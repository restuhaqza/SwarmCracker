// Package agent implements the guest-agent wire protocol, the guest-side
// request handlers (exec, file put, file get) and the host-side client used to
// drive a microVM over Firecracker's vsock Unix socket.
//
// The protocol is a sequence of length-prefixed JSON frames: a 4-byte
// big-endian uint32 body length followed by that many bytes of JSON. The guest
// agent (cmd/sc-agent) serves connections with ServeConn; the host CLI connects
// with Dial and issues requests through Client.
package agent

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	// Version is the protocol version spoken by both the client and the guest
	// agent. A mismatch is rejected during the hello exchange.
	Version = "1"
	// DefaultPort is the vsock port the guest agent listens on.
	DefaultPort uint32 = 52
	// GuestCID is the guest context ID assigned to every microVM. Each VM is
	// its own Firecracker process with its own vsock Unix socket, so the CID
	// need not be host-unique.
	GuestCID = 3
	// MaxFrameSize is the largest accepted frame body (1 MiB). Bodies larger
	// than this are rejected rather than allocated.
	MaxFrameSize = 1 << 20
)

// Request is a single operation sent from the host to the guest agent.
type Request struct {
	Op      string   `json:"op"` // hello|exec|put|get
	Version string   `json:"version,omitempty"`
	Command []string `json:"command,omitempty"`
	TTY     bool     `json:"tty,omitempty"`
	Env     []string `json:"env,omitempty"`
	WorkDir string   `json:"workdir,omitempty"`
	Path    string   `json:"path,omitempty"`
	Mode    uint32   `json:"mode,omitempty"`
}

// Message is a single frame sent from the guest agent back to the host.
type Message struct {
	Type    string `json:"type"` // hello|stdout|stderr|exit|file|ok|error
	Version string `json:"version,omitempty"`
	Data    []byte `json:"data,omitempty"`
	Code    *int   `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
}

// WriteFrame marshals v as JSON and writes it to w prefixed by a 4-byte
// big-endian length. It refuses to write a body larger than MaxFrameSize.
func WriteFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(body) > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes exceeds %d", len(body), MaxFrameSize)
	}

	var header [4]byte
	// Safe: the length check above bounds body to MaxFrameSize (1 MiB).
	binary.BigEndian.PutUint32(header[:], uint32(len(body))) //nolint:gosec // G115: len(body) <= MaxFrameSize
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write frame body: %w", err)
	}
	return nil
}

// ReadFrame reads one length-prefixed JSON frame from r and unmarshals it into
// v. It returns the decoded Message when v is a *Message, so callers can switch
// on Message.Type; for any other target the returned Message is nil.
//
// A zero or oversized length prefix is rejected before any body is allocated.
func ReadFrame(r io.Reader, v any) (*Message, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("read frame header: %w", err)
	}

	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return nil, fmt.Errorf("invalid frame length: 0")
	}
	if length > MaxFrameSize {
		return nil, fmt.Errorf("frame length %d exceeds maximum %d", length, MaxFrameSize)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read frame body: %w", err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return nil, fmt.Errorf("unmarshal frame: %w", err)
	}
	if msg, ok := v.(*Message); ok {
		return msg, nil
	}
	return nil, nil
}
