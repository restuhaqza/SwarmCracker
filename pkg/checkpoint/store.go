package checkpoint

import (
	"context"
	"io"
	"time"
)

// Store is where checkpoints rest: one manifest per checkpoint, and the
// content-addressed chunks those manifests name.
//
// The interface names checkpoints, manifests, chunks and digests, and nothing
// about where they are. A caller never learns a path, a URL or a bucket from it,
// so the chunked format is decided once and the transport is decided later: the
// local driver in this package writes a directory, and an object-store driver
// serves the same interface without a caller changing.
//
// Two rules bind every implementation:
//
//   - A checkpoint or chunk that is not there is reported as an error satisfying
//     errors.Is(err, fs.ErrNotExist), not as an empty value. A caller can then
//     tell "this node never had it" from "it is there and unusable".
//   - A chunk is stored and returned by its content address. PutChunk checks
//     the stream against the digest it is given and refuses a mismatch with
//     ErrCorrupt; Chunk verifies the stored bytes against the digest before the
//     reader reports the chunk's end, so a caller that consumes a chunk to its
//     end either saw its exact bytes or gets ErrCorrupt.
//
// The context is carried for the drivers that need it — a transfer that waits on
// a network — and every driver reports a cancelled context rather than starting
// work.
type Store interface {
	// PutManifest publishes a checkpoint's manifest, replacing the manifest
	// with the same ID. A store keeps the manifest as one document, so a reader
	// sees the previous manifest or the new one, never a partial one.
	PutManifest(ctx context.Context, m Manifest) error

	// Manifest returns the manifest of the checkpoint with this ID.
	Manifest(ctx context.Context, id string) (Manifest, error)

	// PutChunk stores the bytes of one chunk under the digest of those bytes
	// and returns when they are durable. Storing content that is already stored
	// writes nothing: the address exists, so the bytes exist, and no second copy
	// is made. That is what makes a second checkpoint of identical memory (a
	// fork, a warm pool's second VM) cost a manifest and nothing else. The put
	// still counts as a claim on the content, so a driver that reclaims by age
	// must treat content put again as fresh.
	PutChunk(ctx context.Context, digest Digest, r io.Reader) error

	// Chunk returns the bytes stored at digest. The bytes are verified against
	// the digest as they are read; a mismatch is reported as ErrCorrupt.
	Chunk(ctx context.Context, digest Digest) (io.ReadCloser, error)

	// Delete removes the checkpoint with this ID. Deleting a checkpoint that is
	// not there is success: a teardown runs it whether or not the checkpoint was
	// ever published. The chunks it named are shared with other checkpoints, so
	// they are not removed here; Prune reclaims the ones nothing names.
	Delete(ctx context.Context, id string) error

	// List returns the IDs of the checkpoints in the store, sorted ascending, so
	// a caller can walk them with Manifest.
	List(ctx context.Context) ([]string, error)

	// Prune removes the chunks no manifest names, provided they have rested for
	// at least minAge, and reports what it removed. It is how a store reclaims
	// what a delete leaves behind without keeping reference counts: a chunk is
	// kept while any manifest names it, and kept while it is younger than
	// minAge, which is what protects the chunks of a capture that has stored
	// them but not yet published the manifest that names them.
	//
	// minAge must outlast the longest capture a caller allows: a chunk is only
	// protected until its manifest exists, and a capture that reuses an old
	// chunk claims it again (see PutChunk). A store that cannot tell what a
	// manifest references must refuse the pass rather than guess, so a corrupt
	// manifest is an error and nothing is removed.
	Prune(ctx context.Context, minAge time.Duration) (PruneReport, error)
}

// PruneReport is what one Prune pass removed.
type PruneReport struct {
	// Blobs is how many chunks were removed: content no manifest named and
	// that had rested for at least the pass's minAge.
	Blobs int
	// Bytes is how much space those chunks held.
	Bytes int64
}
