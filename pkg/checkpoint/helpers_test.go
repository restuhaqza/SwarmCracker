package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testPin is one well-formed pin: a Firecracker release and a kernel digest, the
// two identities a node records for the software a checkpoint is valid against.
var testPin = Pin{
	Firecracker: "v1.15.1",
	Kernel:      hexDigest([]byte("the guest kernel image")),
}

// otherPin is a pin that differs in both fields, so a test cannot pass by
// comparing only one of them.
var otherPin = Pin{
	Firecracker: "v1.14.0",
	Kernel:      hexDigest([]byte("a different kernel image")),
}

// body returns size deterministic bytes. It is generated rather than repeated so
// a size or ordering mistake shows up as different bytes rather than as a prefix
// that happens to match, and so consecutive chunks of a test image are distinct:
// the high bits of the offset make chunk n differ from chunk n+1, which a
// chunk-aligned period would not.
func body(size int, seed byte) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i*7) + seed + byte(i>>20)
	}
	return b
}

// hexDigest is a chunk digest computed here, so an expectation never comes from
// the code it checks.
func hexDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// testDigest is hexDigest as the type the store takes.
func testDigest(b []byte) Digest { return Digest(hexDigest(b)) }

// zeros is an endless stream of zero bytes, so a test that needs more input than
// it wants to allocate can bound it with io.LimitReader instead.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// uniqueDigests is the set of distinct chunks in ds: one blob each.
func uniqueDigests(ds []Digest) map[Digest]struct{} {
	set := make(map[Digest]struct{}, len(ds))
	for _, d := range ds {
		set[d] = struct{}{}
	}
	return set
}

// chunkDigests splits b at size and returns each piece's digest: the chunk list a
// manifest is expected to hold.
func chunkDigests(b []byte, size int) []Digest {
	out := []Digest{}
	for start := 0; start < len(b); start += size {
		end := start + size
		if end > len(b) {
			end = len(b)
		}
		out = append(out, testDigest(b[start:end]))
	}
	return out
}

// newTestLocal opens a local store over a fresh directory.
func newTestLocal(t *testing.T) *Local {
	t.Helper()
	store, err := NewLocal(LocalConfig{Root: filepath.Join(t.TempDir(), DirName)})
	require.NoError(t, err)
	return store
}

// blobPath is where the layout puts one chunk: <root>/<aa>/<digest>.
func blobPath(root string, d Digest) string {
	return filepath.Join(root, string(d)[:2], string(d))
}

// writeBlob stores raw bytes at a path, so a test can damage a chunk behind the
// store's back.
func writeBlob(t *testing.T, path string, b []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, b, 0o600))
}

// blobCount is how many chunk blobs the store holds, which is how a dedup
// assertion sees a second copy that should not exist.
func blobCount(t *testing.T, root string) int {
	t.Helper()
	return countFiles(t, root, func(name string) bool { return !strings.HasPrefix(name, tempPrefix) })
}

// tempCount is how many half-written files a store left behind.
func tempCount(t *testing.T, root string) int {
	t.Helper()
	return countFiles(t, root, func(name string) bool { return strings.HasPrefix(name, tempPrefix) })
}

// countFiles counts the files under root's chunk shards for which keep reports
// true. The manifests directory is skipped: a test that counts blobs must not
// count manifests.
func countFiles(t *testing.T, root string, keep func(name string) bool) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(root, manifestsDirName) {
				return filepath.SkipDir
			}
			return nil
		}
		if keep(entry.Name()) {
			count++
		}
		return nil
	})
	require.NoError(t, err)
	return count
}
