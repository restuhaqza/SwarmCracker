// Package checkpoint stores a microVM's memory image as fixed-size,
// content-addressed chunks, so a checkpoint can be resumed, forked or shipped to
// another node without ever holding the image in one piece.
//
// A checkpoint here is two things: a manifest and the chunks of vm.mem it
// names. The manifest is a small document — its format version, the image's
// total size, the chunk size, one SHA-256 per chunk in order, and the two pins
// the image is only valid against — so a caller can decide whether it can use a
// checkpoint by reading a few kilobytes, before moving a byte of guest memory.
// It describes the memory image only; anything else a restore needs (the
// Firecracker state document, the disk) is not part of this format.
//
// # Why chunked and content-addressed
//
// A memory image is hundreds of megabytes and every checkpoint of a service
// shares almost all of it with the last one: a fork copies the page tables and
// touches a few pages, and a warm pool's VMs were resumed from the same
// template. Addressing chunks by the SHA-256 of their bytes makes that
// sharing free — a second write of identical content stores no second blob —
// which is what makes forking cheap. The same address is what lets a transfer
// resume (a chunk that already arrived is skipped, a chunk that failed is
// retried alone), and it lets an on-demand loader (a userfaultfd handler)
// fetch exactly the 4 MiB a faulting page needs instead of the whole image.
// Content addressing also verifies itself: a read checks the bytes against the
// address they were stored under, so a flipped byte is an error rather than a
// guest that boots into garbage.
//
// # Pins
//
// A memory image is only resumable by the VMM and guest kernel that wrote it:
// Firecracker's snapshot state names the VMM's internal layout, and the image
// was built against one kernel. So the manifest pins both, and VerifyPin
// refuses a manifest captured on other software. A node compares a manifest
// with its own pin before it forks or restores from it.
//
// # What is not here
//
// The Store interface is deliberately transport-free: it names manifests,
// chunks and checkpoint IDs, never a URL, a bucket or a host path. The local
// driver writes a directory; a later object-store driver serves the same
// interface, and callers do not change. Encryption, retention and reclaiming
// the blobs no manifest names are later work: this package defines the format
// and the local driver that proves it.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	// ChunkSize is the fixed number of bytes one chunk of a memory image holds:
	// 4 MiB. Every chunk of an image is exactly this size except the last,
	// which ends where the image does. It is sized so one chunk is a page-fault
	// round trip for an on-demand loader, a resumable unit for a transfer, and
	// small enough that verifying one holds 4 MiB of memory rather than the
	// image's.
	ChunkSize int = 4 << 20

	// sha256HexLength is the length of a hex-encoded SHA-256 digest.
	sha256HexLength = 64
)

// ErrInvalidManifest classifies every manifest this package refuses: one it
// cannot decode, one that is incomplete, and one that describes an image whose
// chunks cannot be the image's size. The wrapped message says what is wrong, so
// a caller can log it and errors.Is still answers the class.
var ErrInvalidManifest = errors.New("invalid checkpoint manifest")

// ErrPinMismatch classifies a manifest that is well formed but was captured on
// host software this node does not run: another Firecracker version, or another
// guest kernel. Resuming such an image is unsafe, so it is refused rather than
// attempted.
var ErrPinMismatch = errors.New("checkpoint pin does not match this node")

// ErrCorrupt classifies a chunk whose bytes are not the bytes its address
// promises: a blob damaged on disk, truncated, or written by something that is
// not this package. A write also reports it when the caller offers a stream that
// does not hash to the digest it named.
var ErrCorrupt = errors.New("checkpoint chunk is corrupt")

// Digest is a chunk's content address: the lowercase hex SHA-256 of its bytes.
// The manifest names chunks by digest, the store files them under it, and a
// reader checks the bytes it loads against it.
type Digest string

// Valid reports whether d has the only shape this package writes: 64 lowercase
// hex characters. A digest reaches a path and a manifest, so it is checked
// before either.
func (d Digest) Valid() bool {
	if len(d) != sha256HexLength {
		return false
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// digestOf returns the digest of b.
func digestOf(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest(hex.EncodeToString(sum[:]))
}

// Pin names the host software a memory image is only valid against.
//
// Both fields are identities the node already has: the Firecracker version that
// wrote the image, and the guest kernel it was booted with (normally the digest
// of the kernel image, which is what tells two kernels apart when their version
// strings agree). They are compared exactly, so the same value must be recorded
// on the node that captures the checkpoint and the node that resumes it.
type Pin struct {
	// Firecracker is the VMM version the checkpoint was captured with, as
	// `firecracker --version` reports it.
	Firecracker string `json:"firecracker"`
	// Kernel identifies the guest kernel the image was built against, normally
	// its SHA-256 digest.
	Kernel string `json:"kernel"`
}

// Validate reports why the pin is incomplete, or nil. An absent pin is not a
// wildcard: a manifest that pins nothing says nothing about where it can be
// resumed, so it is refused.
func (p Pin) Validate() error {
	if p.Firecracker == "" {
		return errors.New("pin.firecracker is empty: want the Firecracker version the checkpoint was captured with")
	}
	if p.Kernel == "" {
		return errors.New("pin.kernel is empty: want the guest kernel the image was built against, normally its SHA-256 digest")
	}
	return nil
}

// invalidf builds a rejection that carries ErrInvalidManifest, the same shape
// pkg/migration's manifest uses: one sentence, one class.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, args...))
}
