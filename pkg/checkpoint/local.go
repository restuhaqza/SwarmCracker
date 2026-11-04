package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// DirName is the subdirectory of a node's state directory that holds
	// checkpoints. It is a directory of its own so a daemon restart finds
	// exactly what it wrote, and so removing them is one directory rather than
	// a pattern over the node's other state.
	DirName = "checkpoints"

	// manifestsDirName holds one document per checkpoint: <root>/manifests/<id>.json.
	// Chunk shards are named by two hex characters, so no shard can be called
	// this and the two kinds of entry under the root never collide.
	manifestsDirName = "manifests"

	// manifestSuffix is the extension of one manifest. The checkpoint ID is the
	// file name because a lookup is by ID and nothing else.
	manifestSuffix = ".json"

	// tempPrefix, tempSuffix frame the name of anything mid-write: a manifest or
	// a chunk is written to .checkpoint-<random>.tmp beside its destination and
	// renamed into place. The leading dot keeps it out of a listing, and the
	// suffix keeps it from ever being read as a manifest.
	tempPrefix = ".checkpoint-"
	tempSuffix = ".tmp"

	// probePattern names the throwaway file NewLocal writes to prove the root
	// accepts one. It never ends in the manifest suffix, so a crash mid-probe
	// leaves something a human can identify rather than a checkpoint.
	probePattern = ".checkpoint-writable.probe-*"

	// dirMode is the mode of the root, its shards and the manifest directory: a
	// memory image can hold guest secrets, so nobody but the daemon reads one.
	dirMode = 0o700
)

// Dir returns the checkpoint directory for a node whose state directory is
// stateDir, or "" when stateDir is empty. It is one rule so the daemon and a
// test cannot disagree about where checkpoints live.
func Dir(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, DirName)
}

// LocalConfig configures a local store.
type LocalConfig struct {
	// Root is the checkpoint directory, normally Dir(stateDir). It must be
	// non-empty; it is created (0700) when missing and write-probed, because a
	// store that cannot hold a chunk must be known when the node starts, not
	// when it forks a VM.
	Root string
}

// Local is the local-filesystem driver: manifests are documents under
// <root>/manifests, and chunks are content-addressed blobs under
// <root>/<first two characters of the digest>/<digest>.
//
// The layout is content addressing, not checkpoint addressing, because a chunk
// belongs to whoever stored it: two checkpoints that share a page share a blob,
// and neither owns it. That is why Delete removes a checkpoint's manifest and
// leaves the blobs it named — another checkpoint may name them too, and this
// driver keeps no reference counts. Reclaiming blobs no manifest names is a
// sweep for the retention work, not something a per-checkpoint delete can
// decide on its own.
//
// It is safe for concurrent use. Writers create shard directories on demand;
// two writes of the same content write different temporaries and rename onto the
// same address, and a rename of identical bytes leaves one blob either way.
type Local struct {
	root      string
	manifests string
}

// NewLocal opens — creating when needed — the local store and proves it can be
// written to. The root, its shards and the manifests directory are 0700 and the
// files inside them are 0600.
//
// Creating the directory is not enough to know a chunk can be written: MkdirAll
// succeeds on an existing directory whatever its mode or mount (a read-only
// remount, a full filesystem), and PutChunk runs while a VM is being forked. So
// the manifests directory is probed with a real write here, while the daemon can
// still refuse to start instead of failing a fork later.
func NewLocal(cfg LocalConfig) (*Local, error) {
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		return nil, errors.New("checkpoint store: a root directory is required: pass " + DirName + " under the node's state directory")
	}
	root = filepath.Clean(root)
	manifests := filepath.Join(root, manifestsDirName)
	if err := os.MkdirAll(manifests, dirMode); err != nil {
		return nil, fmt.Errorf("checkpoint store: create %s: %w", manifests, err)
	}

	probe, err := os.CreateTemp(manifests, probePattern)
	if err != nil {
		return nil, fmt.Errorf("checkpoint store: %s is not writable: %w", manifests, err)
	}
	probeName := probe.Name()
	_ = probe.Close()
	if err := os.Remove(probeName); err != nil {
		return nil, fmt.Errorf("checkpoint store: %s is not writable: remove the probe file %s: %w", manifests, probeName, err)
	}

	return &Local{root: root, manifests: manifests}, nil
}

// Root returns the store's root directory.
func (l *Local) Root() string { return l.root }

// PutManifest writes one manifest, replacing any manifest with the same ID. The
// manifest is validated first, so an unusable one is refused before anything
// touches the disk, and it is written to a unique temporary in the manifests
// directory, fsynced, renamed into place and the directory is fsynced: a reader
// sees the previous manifest or the new one, and a failed write publishes
// nothing.
func (l *Local) PutManifest(ctx context.Context, m Manifest) error {
	if err := l.ready(ctx); err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := m.encode()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(l.manifests, tempPrefix+"*"+tempSuffix)
	if err != nil {
		return fmt.Errorf("checkpoint store: write manifest %s: create temporary file: %w", m.ID, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		discard()
		return fmt.Errorf("checkpoint store: write manifest %s: %w", m.ID, err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("checkpoint store: sync manifest %s: %w", m.ID, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("checkpoint store: close manifest %s: %w", m.ID, err)
	}
	if err := os.Rename(tmpName, l.manifestPath(m.ID)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("checkpoint store: publish manifest %s: %w", m.ID, err)
	}

	syncDir(l.manifests)
	return nil
}

// Manifest returns one checkpoint's manifest, which is one small document: it is
// read without touching any chunk, so a caller can decide whether it wants the
// image at all. A missing manifest is reported as fs.ErrNotExist; a manifest
// that is there but is not one this package can read is an ErrInvalidManifest.
func (l *Local) Manifest(ctx context.Context, id string) (Manifest, error) {
	if err := l.ready(ctx); err != nil {
		return Manifest{}, err
	}
	if err := validID(id); err != nil {
		return Manifest{}, fmt.Errorf("checkpoint store: id %q is not a checkpoint ID: %w", id, err)
	}

	path := l.manifestPath(id)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, fmt.Errorf("checkpoint %s: manifest %s: %w", id, path, err)
		}
		return Manifest{}, fmt.Errorf("checkpoint %s: read manifest %s: %w", id, path, err)
	}
	defer func() { _ = f.Close() }()

	m, err := decodeManifest(f, path)
	if err != nil {
		return Manifest{}, err
	}
	// A document that decodes is not evidence it is this checkpoint's: a file
	// copied or renamed on disk would otherwise answer for an ID it does not
	// describe, and a caller would resume an image under the wrong name.
	if m.ID != id {
		return Manifest{}, invalidf("manifest %s belongs to %q, not %q", path, m.ID, id)
	}
	return m, nil
}

// PutChunk stores one chunk under the digest of its bytes. Content that is
// already stored is a no-op — that is the whole of dedup: the address exists, so
// the bytes exist, and a fork of an identical memory writes no second blob.
//
// A new chunk is streamed into a temporary beside its destination while its
// digest is computed, so a stream that does not hash to the digest it was given
// is refused with ErrCorrupt and publishes nothing. A complete chunk is fsynced,
// renamed to its address and the shard directory is fsynced, so a reader never
// sees a partial blob and a crash leaves either no blob or the whole one.
func (l *Local) PutChunk(ctx context.Context, digest Digest, r io.Reader) error {
	if err := l.ready(ctx); err != nil {
		return err
	}
	if !digest.Valid() {
		return fmt.Errorf("checkpoint store: %q is not a chunk digest: want 64 lowercase hex characters, the SHA-256 of the chunk's bytes", digest)
	}
	if r == nil {
		return fmt.Errorf("checkpoint chunk %s: a source is required", digest)
	}

	path := l.chunkPath(digest)
	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		// Dedup: the address exists, so the bytes exist. Nothing is written.
		return nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return fmt.Errorf("checkpoint chunk %s: stat %s: %w", digest, path, statErr)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("checkpoint chunk %s: create %s: %w", digest, dir, err)
	}
	tmp, err := os.CreateTemp(dir, tempPrefix+"*"+tempSuffix)
	if err != nil {
		return fmt.Errorf("checkpoint chunk %s: create temporary file: %w", digest, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, sum), r); err != nil {
		discard()
		return fmt.Errorf("checkpoint chunk %s: write %s: %w", digest, tmpName, err)
	}
	if got := Digest(hex.EncodeToString(sum.Sum(nil))); got != digest {
		discard()
		return fmt.Errorf("%w: chunk %s was offered bytes that hash to %s", ErrCorrupt, digest, got)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("checkpoint chunk %s: sync %s: %w", digest, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("checkpoint chunk %s: close %s: %w", digest, tmpName, err)
	}

	// Two writers of the same content race here by design: both rename the same
	// address, and the bytes are identical whatever order they land in.
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("checkpoint chunk %s: publish %s: %w", digest, path, err)
	}

	syncDir(dir)
	return nil
}

// Chunk returns the bytes stored at digest. The blob is verified as it is read:
// the reader hashes what it hands out and, at the end of the chunk, reports
// ErrCorrupt instead of EOF when the bytes do not hash to their address. Nothing
// larger than the caller's buffer is held, so a chunk costs a stream and not an
// allocation of its size.
//
// The digest is checked as the chunk ends, which is one read later than the last
// byte: a caller must treat the error that ends a chunk as the chunk being
// unusable, which is what Read does.
func (l *Local) Chunk(ctx context.Context, digest Digest) (io.ReadCloser, error) {
	if err := l.ready(ctx); err != nil {
		return nil, err
	}
	if !digest.Valid() {
		return nil, fmt.Errorf("checkpoint store: %q is not a chunk digest: want 64 lowercase hex characters, the SHA-256 of the chunk's bytes", digest)
	}

	path := l.chunkPath(digest)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("checkpoint chunk %s: %w", digest, err)
		}
		return nil, fmt.Errorf("checkpoint chunk %s: open %s: %w", digest, path, err)
	}
	return &chunkReader{
		file:   f,
		hash:   sha256.New(),
		digest: digest,
		path:   path,
	}, nil
}

// Delete removes a checkpoint's manifest. Deleting a checkpoint that is not
// there is success: a teardown, a retried cleanup and a sweep all run it, and
// none should fail because the checkpoint is already gone.
//
// The blobs the checkpoint named stay: they are addressed by content, and
// another checkpoint may name the same bytes. See Local.
func (l *Local) Delete(ctx context.Context, id string) error {
	if err := l.ready(ctx); err != nil {
		return err
	}
	if err := validID(id); err != nil {
		return fmt.Errorf("checkpoint store: id %q is not a checkpoint ID: %w", id, err)
	}

	path := l.manifestPath(id)
	err := os.Remove(path)
	switch {
	case err == nil:
		// PutManifest flushes the directory after its rename so the entry
		// survives a crash; a removal needs the same flush, or a crash could
		// bring back a checkpoint the caller believes is gone.
		syncDir(l.manifests)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("checkpoint %s: delete manifest %s: %w", id, path, err)
	}
}

// List returns the IDs of the checkpoints the store holds, sorted ascending.
// It reads one directory, not one document per checkpoint: a manifest that is
// present but unusable is still listed, and Manifest reports what is wrong with
// it when the caller gets to it.
func (l *Local) List(ctx context.Context) ([]string, error) {
	if err := l.ready(ctx); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(l.manifests)
	if err != nil {
		return nil, fmt.Errorf("checkpoint store: read %s: %w", l.manifests, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, manifestSuffix) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, manifestSuffix))
	}
	sort.Strings(ids)
	return ids, nil
}

// ready reports whether the store can be used and the caller still wants the
// work: a cancelled context and a zero-value Local are both refused before
// anything touches the disk.
func (l *Local) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil || l.root == "" {
		return errors.New("checkpoint store: use NewLocal")
	}
	return nil
}

// manifestPath is the document for one checkpoint. Callers validate the ID
// first: it becomes a file name, so a separator or a dot would escape the
// directory.
func (l *Local) manifestPath(id string) string {
	return filepath.Join(l.manifests, id+manifestSuffix)
}

// chunkPath is a blob's address on disk: <root>/<first two characters>/<digest>.
// The two-character shard keeps every directory to a few thousand entries, which
// is what keeps a lookup cheap on a filesystem with a large image in it.
func (l *Local) chunkPath(digest Digest) string {
	return filepath.Join(l.root, string(digest)[:2], string(digest))
}

// chunkReader is one chunk being read: the file, the hash of what was handed
// out, and the address both are checked against at the end.
type chunkReader struct {
	file   *os.File
	hash   hash.Hash
	digest Digest
	path   string

	// err is what the reader ends with: the first failure, or fs.ErrClosed.
	err error
	// closed is set once file is closed, so a second Close does nothing.
	closed bool
}

// Read passes the blob through, hashing what it hands out, and verifies the
// digest where the file ends. The verification error replaces io.EOF, so a
// caller that reads to the end of a chunk cannot mistake a damaged blob for a
// complete one.
func (r *chunkReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}

	n, err := r.file.Read(p)
	if n > 0 {
		_, _ = r.hash.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		if got := Digest(hex.EncodeToString(r.hash.Sum(nil))); got != r.digest {
			r.err = fmt.Errorf("%w: %s holds bytes that hash to %s, not to its address %s", ErrCorrupt, r.path, got, r.digest)
			return n, r.err
		}
	}
	return n, err
}

// Close releases the blob. A read after Close reports fs.ErrClosed; closing
// twice does nothing.
func (r *chunkReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.err == nil {
		r.err = fs.ErrClosed
	}
	return r.file.Close()
}

// syncDir flushes a directory after an entry in it changed (a rename, a
// removal), so the entry survives a crash. It is best effort: the file itself is
// already complete at this point, and a filesystem that cannot sync a directory
// (or a race with another process removing it) must not turn a successful write
// into an error.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
