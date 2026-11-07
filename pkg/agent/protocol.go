// Package agent implements the guest-agent wire protocol, the guest-side
// request handlers (exec, file put, file get, prepare) and the host-side client
// used to drive a microVM over Firecracker's vsock Unix socket.
//
// The protocol is a sequence of length-prefixed JSON frames: a 4-byte
// big-endian uint32 body length followed by that many bytes of JSON. The guest
// agent (cmd/sc-agent) serves connections with ServeConn; the host CLI connects
// with Dial and issues requests through Client.
//
// Version 2 makes the handshake authoritative. A hello reply names the protocol
// versions and the capabilities the guest advertises, together with the agent
// version, build and boot identity (see Hello). Every request carries an id,
// echoed on every frame it produces, and a relative deadline in milliseconds;
// every failure carries a typed code (see Error). A host never issues an
// operation the guest did not advertise, and never substitutes a narrower
// operation for the one it wanted: an agent that cannot serve the operation is
// refused during the handshake, so a newly added operation can never be skipped
// silently.
package agent

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// Version is the protocol version this build speaks.
	Version = "2"
	// VersionLegacy is the pre-v2 protocol: hello compared a bare version string
	// and advertised no capabilities, request ids, deadlines or boot identity.
	// A v2 host treats every peer speaking it as unsupported rather than
	// guessing what it can do.
	VersionLegacy = "1"
	// DefaultPort is the vsock port the guest agent listens on.
	DefaultPort uint32 = 52
	// GuestCID is the guest context ID assigned to every microVM. Each VM is
	// its own Firecracker process with its own vsock Unix socket, so the CID
	// need not be host-unique.
	GuestCID = 3
	// MaxFrameSize is the largest accepted control-frame body (1 MiB). Bodies
	// larger than this are rejected rather than allocated. Streaming
	// operations use MaxStreamFrameSize instead.
	MaxFrameSize = 1 << 20
	// MaxStreamFrameSize is the largest accepted frame body for a streaming
	// operation (exec output, put/get payloads, and the tar stream that will
	// reuse them). It is its own bound so that raising a stream limit can never
	// widen what a control frame may allocate.
	MaxStreamFrameSize = 4 << 20
)

// Capability names, one per operation in the closed v2 set. A host must not
// issue an operation that the guest did not advertise, so an operation added to
// a later release is refused explicitly by an older guest instead of being
// approximated with one it does have.
const (
	CapHello   = "hello"
	CapExec    = "exec"
	CapPut     = "put"
	CapGet     = "get"
	CapMetrics = "metrics"
	// CapPrepare is the post-restore hook: the guest reads the identity the node
	// published to its metadata service and applies it to itself (prepare.go).
	CapPrepare = "prepare"
)

// capabilities is the closed set of operations this agent build implements. It
// must list exactly the ops dispatch serves: an op that is served but not
// advertised can never be called, and an op that is advertised but not served
// would fail after the host committed to it.
var capabilities = []string{CapHello, CapExec, CapPut, CapGet, CapMetrics, CapPrepare}

// Capabilities returns the operations this agent build advertises. The result is
// a copy, so a caller cannot change what the agent serves.
func Capabilities() []string { return append([]string(nil), capabilities...) }

// ErrorCode is a stable, machine-readable failure code. It is the contract
// between host and guest; the message text next to it is not.
type ErrorCode string

const (
	// CodeInvalidRequest reports a malformed request, such as a put with no path.
	CodeInvalidRequest ErrorCode = "invalid_request"
	// CodeUnsupportedOp reports an operation the peer did not advertise.
	CodeUnsupportedOp ErrorCode = "unsupported_op"
	// CodeProtocolUnsupported reports a peer speaking a protocol this side
	// cannot negotiate, including every agent older than v2.
	CodeProtocolUnsupported ErrorCode = "agent_protocol_unsupported"
	// CodeDeadlineExceeded reports a request whose budget was spent before it
	// arrived, or an operation that outlived its deadline.
	CodeDeadlineExceeded ErrorCode = "deadline_exceeded"
	// CodeProtocolError reports a frame that breaks the framing contract, such
	// as a reply that does not echo the request id.
	CodeProtocolError ErrorCode = "protocol_error"
	// CodeInternal reports a failure inside the guest while carrying out an
	// operation it does advertise.
	CodeInternal ErrorCode = "internal"
	// CodePrepareFailed reports that the post-restore hook ran and failed: the
	// guest could not read the identity its node published, or could not apply
	// it. It is a failed launch, never a warning, because an instance wearing its
	// source's identity is not the instance the node asked for.
	CodePrepareFailed ErrorCode = "prepare_failed"
)

// Error is a typed protocol failure. Code is the part a caller branches on;
// Message is for humans and may change.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// Error implements error.
func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// CodeOf returns the typed code carried by err, or the empty code when err did
// not come from this protocol.
func CodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Hello is the outcome of the v2 handshake: what the guest speaks and what it
// can do. A host keeps it for the life of the connection and refuses any
// operation missing from Caps.
type Hello struct {
	// Version is the negotiated protocol version.
	Version string
	// Versions lists every protocol version the guest speaks.
	Versions []string
	// Caps lists the operations the guest advertises.
	Caps []string
	// AgentVersion is the guest agent's release version.
	AgentVersion string
	// BuildID identifies the exact agent build, normally the git commit.
	BuildID string
	// BootID identifies this guest instance. It changes when the guest reboots
	// and when the restore path rotates it, so a host holding a session from
	// before a restore can recognise that it describes a different guest.
	BootID string
}

// Supports reports whether the guest advertised op.
func (h Hello) Supports(op string) bool {
	for _, c := range h.Caps {
		if c == op {
			return true
		}
	}
	return false
}

// HelloReply builds the canonical v2 hello payload for a guest advertising caps.
// It is built in one place so every agent — the real guest and any test double
// standing in for it — describes the same contract. The caller stamps the id of
// the hello request it answers.
func HelloReply(caps []string) Message {
	return Message{
		Type:         "hello",
		Version:      Version,
		Versions:     []string{Version},
		Caps:         append([]string(nil), caps...),
		AgentVersion: AgentVersion,
		BuildID:      BuildID,
		BootID:       BootID(),
	}
}

// Request is a single operation sent from the host to the guest agent.
type Request struct {
	Op string `json:"op"` // hello|exec|put|get|metrics|prepare
	// ID correlates every frame this request produces. The guest echoes it
	// verbatim, so a host can tell a reply from one belonging to a request it
	// has already abandoned.
	ID string `json:"id,omitempty"`
	// DeadlineMs is the caller's remaining budget for the operation, in
	// milliseconds, measured when the frame was written. The guest runs on a
	// different clock, so a relative budget crosses the wire where an absolute
	// instant would not survive a snapshot restore. Zero means no deadline; a
	// negative value means the budget was already spent and the guest answers
	// deadline_exceeded without acting.
	DeadlineMs int64    `json:"deadlineMs,omitempty"`
	Version    string   `json:"version,omitempty"`
	Command    []string `json:"command,omitempty"`
	TTY        bool     `json:"tty,omitempty"`
	Env        []string `json:"env,omitempty"`
	WorkDir    string   `json:"workdir,omitempty"`
	Path       string   `json:"path,omitempty"`
	Mode       uint32   `json:"mode,omitempty"`
	// Token is the per-boot credential the node provisioned into this guest's
	// rootfs. It is carried only by hello; a guest agent with a token configured
	// closes the connection without a frame when it is missing or wrong.
	Token string `json:"token,omitempty"`
}

// Message is a single frame sent from the guest agent back to the host.
type Message struct {
	Type string `json:"type"` // hello|stdout|stderr|exit|file|ok|error|metrics|prepared
	// ID echoes the id of the request this frame answers.
	ID string `json:"id,omitempty"`
	// Version is the protocol version the guest speaks, set on a hello reply.
	Version string `json:"version,omitempty"`
	// AuthRequired reports that this agent verifies a per-boot token. It is set
	// only on a hello reply, and omitted by an agent that predates token
	// authentication, so an old reply stays byte-identical.
	AuthRequired bool `json:"auth_required,omitempty"`
	// TokenOK reports that the token in the hello request matched this boot's
	// token. A rejected token gets no frame at all, so a hello reply without
	// this field means the agent ignored the token.
	TokenOK bool `json:"token_ok,omitempty"`
	// Versions, Caps, AgentVersion, BuildID and BootID are set on a hello reply.
	Versions     []string `json:"versions,omitempty"`
	Caps         []string `json:"caps,omitempty"`
	AgentVersion string   `json:"agentVersion,omitempty"`
	BuildID      string   `json:"buildId,omitempty"`
	BootID       string   `json:"bootId,omitempty"`
	Data         []byte   `json:"data,omitempty"`
	Code         *int     `json:"code,omitempty"`
	// Signal is set on an exit frame when the guest process was terminated by a
	// signal. A normal exit omits it.
	Signal *int `json:"signal,omitempty"`
	// Error is the human-readable failure message on an error frame.
	Error string `json:"error,omitempty"`
	// ErrorCode is the typed failure code on an error frame. It is separate
	// from Code because an exit frame already uses "code" for the process exit
	// status, and the two never appear on the same frame.
	ErrorCode ErrorCode `json:"errorCode,omitempty"`
	// Metrics is set on the reply to a metrics request.
	Metrics *GuestMetrics `json:"metrics,omitempty"`
	// Prepared is set on the reply to a prepare request: what the guest carries
	// after it applied the identity its node published.
	Prepared *PrepareReport `json:"prepared,omitempty"`
}

// WriteFrame marshals v as JSON and writes it to w prefixed by a 4-byte
// big-endian length. It refuses to write a body larger than MaxFrameSize.
//
// Every writer in this package chunks its payload at fileChunkSize, far below
// MaxFrameSize, so a stream frame is never written near either bound; the
// stream bound only widens what a reader accepts.
func WriteFrame(w io.Writer, v any) error {
	return writeFrame(w, v, MaxFrameSize)
}

func writeFrame(w io.Writer, v any, max int) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(body) > max {
		return fmt.Errorf("frame too large: %d bytes exceeds %d", len(body), max)
	}

	var header [4]byte
	// Safe: the length check above bounds body to max.
	binary.BigEndian.PutUint32(header[:], uint32(len(body))) //nolint:gosec // G115: len(body) <= max
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write frame body: %w", err)
	}
	return nil
}

// ReadFrame reads one length-prefixed JSON control frame from r and unmarshals
// it into v. It returns the decoded Message when v is a *Message, so callers can
// switch on Message.Type; for any other target the returned Message is nil.
//
// A zero or oversized length prefix is rejected before any body is allocated.
func ReadFrame(r io.Reader, v any) (*Message, error) {
	return readFrame(r, v, MaxFrameSize)
}

// ReadStreamFrame reads one frame under the streaming bound. Streaming
// operations (exec output, put and get payloads) use it so their frames are
// bounded by MaxStreamFrameSize instead of the control-frame cap.
func ReadStreamFrame(r io.Reader, v any) (*Message, error) {
	return readFrame(r, v, MaxStreamFrameSize)
}

func readFrame(r io.Reader, v any, max int) (*Message, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("read frame header: %w", err)
	}

	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return nil, fmt.Errorf("invalid frame length: 0")
	}
	if int64(length) > int64(max) {
		return nil, fmt.Errorf("frame length %d exceeds maximum %d", length, max)
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
