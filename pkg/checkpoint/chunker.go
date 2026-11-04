package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

// Write streams one checkpoint into store and returns the manifest it
// published: state is the Firecracker state document (vm.state) and memory the
// guest memory image (vm.mem).
//
// Nothing holds either document. The state document is read once through a hash
// to record its size and digest, and the image is read through one ChunkSize
// buffer, each full buffer stored as a chunk, with the manifest assembled from
// the digests as they are computed. So a caller needs 4 MiB of headroom to
// checkpoint a guest with any amount of memory, and the memory source may be a
// file, a pipe or a Firecracker memory dump being written.
//
// The checkpoint is published last: chunks are stored first, and the manifest is
// written only when the state document was read and the whole image was stored.
// A write that fails part-way leaves its chunks in the store, where they are
// addressed and may be reused, but no manifest names them, so no caller can read
// the checkpoint as a whole.
//
// The ID, the pin and the state document are checked before the first chunk is
// stored, because a checkpoint that could never be published must not cost a
// gigabyte of writes first. The ID and pin of an existing checkpoint are not
// compared: writing the same ID again replaces its manifest, which is how a
// retried capture finishes.
func Write(ctx context.Context, store Store, id string, pin Pin, state, memory io.Reader) (Manifest, error) {
	if store == nil {
		return Manifest{}, fmt.Errorf("checkpoint %s: a store is required: pass the driver to write to", id)
	}
	if state == nil {
		return Manifest{}, fmt.Errorf("checkpoint %s: a state document is required: pass the vm.state the memory image resumes from", id)
	}
	if memory == nil {
		return Manifest{}, fmt.Errorf("checkpoint %s: a memory image is required: pass the vm.mem to read", id)
	}
	if err := validID(id); err != nil {
		return Manifest{}, invalidf("id %q is not a checkpoint ID: %v", id, err)
	}
	if err := pin.Validate(); err != nil {
		return Manifest{}, invalidf("checkpoint %s: %v", id, err)
	}
	described, err := describeState(state)
	if err != nil {
		return Manifest{}, fmt.Errorf("checkpoint %s: %w", id, err)
	}

	m := Manifest{
		FormatVersion: FormatVersion,
		ID:            id,
		State:         described,
		ChunkSize:     ChunkSize,
		Chunks:        []Digest{},
		Pin:           pin,
	}

	buf := make([]byte, ChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return Manifest{}, fmt.Errorf("checkpoint %s: write: %w", id, err)
		}

		// ReadFull fills the buffer or ends the image: the last chunk is the
		// short read before EOF, and an image that is a multiple of ChunkSize
		// ends on the next call with nothing to store.
		n, err := io.ReadFull(memory, buf)
		if n > 0 {
			digest := digestOf(buf[:n])
			if putErr := store.PutChunk(ctx, digest, bytes.NewReader(buf[:n])); putErr != nil {
				return Manifest{}, fmt.Errorf("checkpoint %s: store chunk %d: %w", id, len(m.Chunks), putErr)
			}
			m.Chunks = append(m.Chunks, digest)
			m.TotalSize += int64(n)
			// Refused while reading rather than at Validate: the ceiling exists
			// so a nonsense source is not streamed into the store first.
			if m.TotalSize > maxMemorySize {
				return Manifest{}, invalidf("checkpoint %s: the image is larger than %d bytes (256 GiB), the largest image a checkpoint describes", id, maxMemorySize)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return Manifest{}, fmt.Errorf("checkpoint %s: read the memory image at byte %d: %w", id, m.TotalSize, err)
		}
	}

	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	if err := store.PutManifest(ctx, m); err != nil {
		return Manifest{}, fmt.Errorf("checkpoint %s: publish manifest: %w", id, err)
	}
	return m, nil
}

// describeState reads the state document and returns its description. The
// document is streamed through a hash, so its size costs one read buffer and
// not an allocation of its size, and it is refused past maxStateSize before the
// caller is made to read more than that.
func describeState(state io.Reader) (State, error) {
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(state, maxStateSize+1))
	if err != nil {
		return State{}, fmt.Errorf("read the state document: %w", err)
	}
	switch {
	case n == 0:
		return State{}, errors.New("the state document is empty: want the vm.state snapshot/create wrote")
	case n > maxStateSize:
		return State{}, fmt.Errorf("the state document is larger than %d bytes (64 MiB): want the vm.state snapshot/create wrote", maxStateSize)
	}
	return State{Size: n, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}

// verifyState reads the state document a caller offers and refuses one that is
// not the document the manifest describes. The size is compared as well as the
// digest, so a truncated document is reported as the size it is rather than only
// as a hash that differs.
func verifyState(m Manifest, state io.Reader) error {
	if state == nil {
		return fmt.Errorf("checkpoint %s: a state document is required: pass the vm.state the checkpoint resumes from", m.ID)
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(state, m.State.Size+1))
	if err != nil {
		return fmt.Errorf("checkpoint %s: read the state document: %w", m.ID, err)
	}
	got := State{Size: n, SHA256: hex.EncodeToString(sum.Sum(nil))}
	if got != m.State {
		return fmt.Errorf("%w: checkpoint %s was captured with a %d-byte state document whose sha256 is %s, and the document offered is %d bytes whose sha256 is %s: a checkpoint is a pair, and another checkpoint's state resumes the wrong VM",
			ErrStateMismatch, m.ID, m.State.Size, m.State.SHA256, got.Size, got.SHA256)
	}
	return nil
}

// Read returns the memory image the manifest describes, streamed chunk by
// chunk: one chunk is held by the reader at a time, so consuming an image of any
// size needs a caller's read buffer and no more.
//
// state is the checkpoint's vm.state document, which Read consumes and checks
// against the manifest before it returns anything; a document from another
// checkpoint, or one that changed after the capture, is refused with
// ErrStateMismatch. Pairing is checked here rather than left to the caller
// because the mistake it prevents — resuming an image on another checkpoint's
// state — produces a running VM and no error.
//
// The manifest is validated first, so a checkpoint that could not have been
// written is refused before the store is asked for anything. Each chunk is then
// fetched by digest and verified by the store as it ends, and the reader never
// hands out more than the length the manifest implies. A chunk that is missing,
// is shorter or longer than the manifest says, or does not hash to its address
// ends the read with an error. A caller that reached io.EOF read exactly the
// bytes the manifest describes.
func Read(ctx context.Context, store Store, m Manifest, state io.Reader) (io.ReadCloser, error) {
	if store == nil {
		return nil, fmt.Errorf("checkpoint %s: a store is required: pass the driver to read from", m.ID)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := verifyState(m, state); err != nil {
		return nil, err
	}
	return &imageReader{ctx: ctx, store: store, m: m}, nil
}

// imageReader is the io.ReadCloser Read returns: the manifest's chunks, in
// order, as one stream of exactly the manifest's total size.
type imageReader struct {
	ctx   context.Context
	store Store
	m     Manifest

	// next is the index of the chunk being read, and got is how many of its
	// bytes were handed out so far.
	next int
	cur  io.ReadCloser
	got  int64

	// err is the error the reader ends with: the first failure, or fs.ErrClosed
	// once it is closed. A reader that holds one is done.
	err error
}

// Read serves the image from the current chunk, opening the next one at each
// boundary. A chunk is finished — its end proved and its digest verified by the
// store — before the next is opened, so a damaged blob stops the read where it
// is rather than after the image.
func (r *imageReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}

	for {
		if r.cur == nil {
			if r.next >= len(r.m.Chunks) {
				return 0, io.EOF
			}
			chunk, err := r.store.Chunk(r.ctx, r.m.Chunks[r.next])
			if err != nil {
				r.err = fmt.Errorf("checkpoint %s: open chunk %d of %d: %w", r.m.ID, r.next, len(r.m.Chunks), err)
				return 0, r.err
			}
			r.cur, r.got = chunk, 0
		}

		// Every byte the manifest describes for this chunk has been handed out:
		// prove the blob ends here, which is also the read that verifies it.
		want := r.m.chunkLength(r.next)
		if r.got == want {
			if err := r.finish(want); err != nil {
				return 0, err
			}
			continue
		}

		buf := p
		if rest := want - r.got; int64(len(buf)) > rest {
			buf = buf[:rest]
		}
		n, err := r.cur.Read(buf)
		r.got += int64(n)
		switch {
		case err == nil:
			if n > 0 {
				return n, nil
			}
			// A reader with nothing to say yet: ask again.

		case errors.Is(err, io.EOF):
			// The blob ended before the manifest's length. A complete blob also
			// ends with EOF, but only at its declared length, which finish
			// handles.
			r.err = fmt.Errorf("checkpoint %s: chunk %d ended after %d bytes, want %d: the image is not the one the manifest describes", r.m.ID, r.next, r.got, want)
			r.dropCur()
			return n, r.err

		default:
			r.err = fmt.Errorf("checkpoint %s: read chunk %d: %w", r.m.ID, r.next, err)
			r.dropCur()
			return n, r.err
		}
	}
}

// finish ends the current chunk whose declared bytes were all handed out. The
// next read is the one that decides: io.EOF means the blob is exactly the
// declared chunk (and, for a verifying store, that it hashed to its address), a
// byte means the blob is longer than the manifest describes, and an error is the
// store's verdict on a damaged blob.
func (r *imageReader) finish(want int64) error {
	var probe [1]byte
	for {
		n, err := r.cur.Read(probe[:])
		switch {
		case n > 0:
			r.err = fmt.Errorf("checkpoint %s: chunk %d is longer than the %d bytes the manifest describes", r.m.ID, r.next, want)
		case errors.Is(err, io.EOF):
			r.dropCur()
			r.next++
			r.got = 0
			return nil
		case err != nil:
			r.err = fmt.Errorf("checkpoint %s: read chunk %d: %w", r.m.ID, r.next, err)
		}
		if r.err != nil {
			r.dropCur()
			return r.err
		}
	}
}

// Close releases the chunk being read. A read after Close reports fs.ErrClosed;
// closing twice does nothing.
func (r *imageReader) Close() error {
	r.dropCur()
	if r.err == nil {
		r.err = fs.ErrClosed
	}
	return nil
}

// dropCur closes and forgets the current chunk. A close error is not reported:
// the bytes were already verified, and a reader that failed to close a chunk
// has nothing left to do about it.
func (r *imageReader) dropCur() {
	if r.cur == nil {
		return
	}
	_ = r.cur.Close()
	r.cur = nil
}
