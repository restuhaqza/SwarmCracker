package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// FormatVersion is the manifest format this build writes and the only one
	// it reads. It moves only when a reader of the previous version would
	// misread the document; a field added later does not move it.
	FormatVersion = 1

	// maxManifestBytes bounds the manifest this package reads. A manifest of
	// the largest image this package describes at the chunk size it writes is a
	// few megabytes, so a larger file is not one of these — and refusing it
	// before decoding keeps a planted file from being read into memory.
	maxManifestBytes = 16 << 20

	// maxMemorySize is the largest memory image one manifest may describe:
	// 256 GiB. It is a ceiling on what a caller can be made to reserve and
	// chunk, not a guess at the largest guest a node runs.
	maxMemorySize int64 = 256 << 30

	// maxStateSize is the largest state document a manifest may describe: 64
	// MiB. A state document is kilobytes; the ceiling is what keeps a manifest
	// from making a caller hash gigabytes of it.
	maxStateSize int64 = 64 << 20

	// maxIDBytes is the longest checkpoint ID. An ID is one path segment and a
	// name for humans, not a description of the checkpoint.
	maxIDBytes = 128
)

// Manifest is one checkpoint's index: the state document it resumes from, the
// shape of its memory image, the chunks that make it up, and the software it is
// valid against.
//
// It is the only thing a caller needs before touching guest memory. It is
// stored as its own small document, so reading it costs kilobytes however large
// the image is, and a node can refuse a checkpoint it cannot resume (see
// VerifyPin) without downloading the image to find out.
type Manifest struct {
	// FormatVersion is the manifest format; see FormatVersion.
	FormatVersion int `json:"formatVersion"`

	// ID is this checkpoint's identity. It is also the manifest's file name in
	// a store, so it is one safe path segment.
	ID string `json:"id"`

	// TotalSize is the length of the memory image in bytes: the sum of every
	// chunk's length. It is what the restored VM must map, so it is stated
	// rather than derived from the chunks, whose lengths a store does not
	// report.
	TotalSize int64 `json:"totalSize"`

	// State is the vm.state document this checkpoint resumes from: its size and
	// digest. The document itself is the caller's, not the store's; Read checks
	// the one it is given against this description.
	State State `json:"state"`

	// ChunkSize is the length of every chunk but the last, in bytes. It is
	// written so a reader can say what it expects and refuse a document it
	// would misread: this build reads ChunkSize, and a manifest written with
	// another chunk size is a manifest from another build (see Validate).
	ChunkSize int `json:"chunkSize"`

	// Chunks are the chunks of the image in order, addressed by the SHA-256 of
	// their bytes. The list is the image: its order is the image's byte order,
	// and a digest may repeat (a zero-filled region is many chunks with one
	// address, and one stored blob).
	Chunks []Digest `json:"chunks"`

	// Pin is the Firecracker version and kernel the image is valid against.
	Pin Pin `json:"pin"`
}

// State describes the Firecracker state document a checkpoint resumes from: the
// vm.state file snapshot/create wrote. It is a description only — the document
// is stored by whoever owns the snapshot — and it exists so a memory image can
// never be loaded with another checkpoint's state.
type State struct {
	// Size is the document's length in bytes.
	Size int64 `json:"size"`
	// SHA256 is the document's content address: 64 lowercase hex characters.
	SHA256 string `json:"sha256"`
}

// Validate reports why the description is incomplete, or nil.
func (s State) Validate() error {
	switch {
	case s.Size <= 0:
		return fmt.Errorf("size is %d: want the length of the vm.state document in bytes", s.Size)
	case s.Size > maxStateSize:
		return fmt.Errorf("size is %d: want at most %d bytes (64 MiB), the largest state document a checkpoint describes", s.Size, maxStateSize)
	}
	if !Digest(s.SHA256).Valid() {
		return fmt.Errorf("sha256 %q is not a digest: want 64 lowercase hex characters, the SHA-256 of the document's bytes", s.SHA256)
	}
	return nil
}

// Validate reports whether the manifest describes an image this package could
// have written and could read back. It checks the description, never the blobs:
// a manifest that passes still needs every chunk to exist and to hash to its
// address before an image can be produced.
func (m Manifest) Validate() error {
	if m.FormatVersion != FormatVersion {
		return invalidf("formatVersion is %d: want %d, the format this build reads and writes", m.FormatVersion, FormatVersion)
	}
	if err := validID(m.ID); err != nil {
		return invalidf("id %q is not a checkpoint ID: %v", m.ID, err)
	}
	switch {
	case m.TotalSize < 0:
		return invalidf("totalSize is %d: want the length of the memory image in bytes", m.TotalSize)
	case m.TotalSize > maxMemorySize:
		return invalidf("totalSize is %d: want at most %d bytes (256 GiB), the largest image a checkpoint describes", m.TotalSize, maxMemorySize)
	}
	// The chunk size is not free-form. A reader that honoured any size would
	// split an image written by another build with this build's arithmetic and
	// produce bytes that look like an image but are not one, so the only size a
	// manifest may name is the one this build chunks with. A future change to
	// ChunkSize therefore moves FormatVersion and lands as a version this build
	// refuses, rather than as a silently wrong image.
	if m.ChunkSize != ChunkSize {
		return invalidf("chunkSize is %d: this build reads %d-byte chunks, so a manifest written with another chunk size was captured by another build and cannot be read by this one", m.ChunkSize, ChunkSize)
	}

	// The count is not a separate field: it is what the size and the chunk size
	// imply, so a manifest cannot describe an image whose last chunk is missing
	// or whose chunks run past the end.
	if want := chunkCount(m.TotalSize, m.ChunkSize); int64(len(m.Chunks)) != want {
		return invalidf("chunks has %d entries: %d bytes at %d bytes per chunk need %d, the last chunk carrying the remainder",
			len(m.Chunks), m.TotalSize, m.ChunkSize, want)
	}
	for i, d := range m.Chunks {
		if !d.Valid() {
			return invalidf("chunks[%d] is %q: want 64 lowercase hex characters, the SHA-256 of the chunk's bytes", i, d)
		}
	}
	if err := m.State.Validate(); err != nil {
		return invalidf("checkpoint %s: state: %v", m.ID, err)
	}
	if err := m.Pin.Validate(); err != nil {
		return invalidf("checkpoint %s: %v", m.ID, err)
	}
	return nil
}

// VerifyPin reports whether this node can resume the checkpoint: pin is what the
// node runs, and a manifest pinned to anything else is refused with
// ErrPinMismatch. Both fields must agree; a Firecracker version match says
// nothing about the kernel the image was built against.
//
// The check is the caller's to make — a Store read does not have a node's pin —
// so a restore or a fork calls it before it loads anything.
func (m Manifest) VerifyPin(pin Pin) error {
	if err := pin.Validate(); err != nil {
		return fmt.Errorf("checkpoint %s: this node's pin is incomplete: %v", m.ID, err)
	}
	if m.Pin == pin {
		return nil
	}
	return fmt.Errorf("%w: checkpoint %s was captured on Firecracker %q with kernel %q, and this node runs Firecracker %q with kernel %q: resume it only where it was captured, or capture a new checkpoint here",
		ErrPinMismatch, m.ID, m.Pin.Firecracker, m.Pin.Kernel, pin.Firecracker, pin.Kernel)
}

// chunkCount is how many chunks an image of total bytes takes at size bytes per
// chunk: the ceiling, because the last chunk carries the remainder. An empty
// image has no chunks.
func chunkCount(total int64, size int) int64 {
	if total <= 0 || size <= 0 {
		return 0
	}
	return (total + int64(size) - 1) / int64(size)
}

// chunkLength is the length of the chunk at index i, or 0 when i names no
// chunk of this manifest. Every chunk is ChunkSize long but the last, which
// ends where the image does.
func (m Manifest) chunkLength(i int) int64 {
	if i < 0 || i >= len(m.Chunks) {
		return 0
	}
	rest := m.TotalSize - int64(i)*int64(m.ChunkSize)
	if rest > int64(m.ChunkSize) {
		return int64(m.ChunkSize)
	}
	return rest
}

// validID reports why id cannot name a checkpoint, or nil. The ID becomes a
// manifest's file name, so it must be one directory entry: a separator or a
// dot would address something other than this store's manifest, and a control
// character would be escaped or hidden wherever the ID is printed.
func validID(id string) error {
	switch {
	case id == "":
		return errors.New("it is empty")
	case id == "." || id == "..":
		return fmt.Errorf("%q is not a name", id)
	case len(id) > maxIDBytes:
		return fmt.Errorf("it is %d bytes and a checkpoint ID may be at most %d", len(id), maxIDBytes)
	case strings.ContainsAny(id, `/\`):
		return errors.New("a checkpoint ID has no path separator")
	case strings.IndexFunc(id, isControl) >= 0:
		return errors.New("a checkpoint ID has no control character")
	}
	return nil
}

// isControl reports whether r is a C0 or DEL character.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f
}

// encode returns the manifest as the document a store writes: indented, because
// a manifest is read during incidents as often as by a program.
func (m Manifest) encode() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest %s: %w", m.ID, err)
	}
	return data, nil
}

// decodeManifest reads one manifest from r and validates it. source names where
// it came from, so a rejection says which file to look at.
func decodeManifest(r io.Reader, source string) (Manifest, error) {
	var m Manifest

	body, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read checkpoint manifest %s: %w", source, err)
	}
	if len(body) > maxManifestBytes {
		return Manifest{}, invalidf("manifest %s is larger than %d bytes: a manifest describes an image, so this is not one", source, maxManifestBytes)
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, invalidf("manifest %s does not decode: %v", source, err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("manifest %s: %w", source, err)
	}
	return m, nil
}
