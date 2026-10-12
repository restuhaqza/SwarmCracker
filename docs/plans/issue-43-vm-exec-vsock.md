# Plan — #43: `vm exec` + file copy via a guest agent (vsock)

Plan id: `issue-43-vm-exec-vsock`
Branch: `feat/vm-exec-vsock`
Repo: github.com/restuhaqza/swarmcracker

## Why

#43 is the blocker for #39 (container healthchecks): there is no way to run a
command inside a microVM. It also delivers Docker-parity `vm exec` / `vm cp`.
Part of Tier 2 in `docs/roadmap.md`.

## What ships (acceptance criteria)

1. `swarmcracker vm exec <vm> -- <cmd>` runs the command in the guest and returns
   exit code, stdout, and stderr.
2. `-it` provides an interactive TTY.
3. `swarmcracker vm cp <src> <vm>:<dst>` and the reverse direction both work.
4. The agent starts automatically (via init) and the CLI checks agent version
   compatibility, failing clearly on mismatch.
5. The agent listens on vsock only (no extra host port) and is documented as a
   trusted control path.
6. Tests + a KVM e2e run; docs updated (`reference/cli.md`).

## Architecture (fixed decisions)

- **Guest agent** `cmd/sc-agent`: static linux/amd64 Go binary, listens on
  AF_VSOCK, serves a framed JSON protocol.
- **Protocol + client** `pkg/agent`: portable Go. Framing = 4-byte big-endian
  length + JSON. Ops: `hello`, `exec`, `put`, `get`.
- **Transport**: Firecracker `PUT /vsock {guest_cid, uds_path}`. For a
  **host-initiated** connection the host dials the muxer at `uds_path` itself
  (NOT `<uds_path>_<port>`), sends `CONNECT <port>\n`, and reads
  `OK <host-side-port>\n` where the returned port is an *arbitrary* assigned
  value (accept any numeric port). `<uds_path>_<port>` exists only for
  **guest-initiated** connections.
- **Delivery**: embed the built agent like tini
  (`pkg/image/binaries/sc-agent-amd64`, `//go:embed`), write `/sbin/sc-agent`,
  start it from the init wrapper.

## Global constraints (verbatim; binding on every task and reviewer)

- Go 1.26. Linux-only runtime. Cross-compile with
  `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`.
- Module path `github.com/restuhaqza/swarmcracker`.
- **No new third-party dependencies.** PTY via `golang.org/x/sys/unix` (already a
  dependency).
- Linux-only files carry `//go:build linux`; provide a non-linux stub when the
  package must still compile.
- Constants (use exactly):
  - `agent.Version = "1"`
  - `agent.DefaultPort uint32 = 52`
  - `agent.GuestCID = 3`
  - `agent.MaxFrameSize = 1 << 20` (1 MiB)
  - guest agent path `/sbin/sc-agent`
  - vsock UDS path `<socketDir>/<task-id>.vsock`; the host **dials that muxer
    path directly**, then sends `CONNECT 52\n` and reads `OK <any-port>\n`
  - framing: 4-byte big-endian uint32 length prefix + JSON body
- Errors wrapped: `fmt.Errorf("<context>: %w", err)`. Never swallow.
- Logging: `github.com/rs/zerolog/log`.
- Tests: table-driven where practical; every test asserts behaviour;
  `go test -short -race` must pass.
- Coverage: total `./pkg/...` must not drop more than 0.1% vs `main`. Keep
  non-testable syscall glue (AF_VSOCK listen, `main`) in `cmd/sc-agent`
  (cmd/ is not coverage-gated).
- `make fmt`; golangci-lint (incl. `unused`) clean; `go mod tidy -diff` clean.
- Never touch `.impeccable/review/`.

## Tasks

### T1 — `pkg/agent`: protocol, guest server, host client

**Goal.** One testable package with the wire protocol, the guest-side handlers
(exec/put/get), the host-side client (Firecracker handshake + exec/copy), and a
minimal PTY.

**Files.** `pkg/agent/protocol.go`, `pkg/agent/server.go`, `pkg/agent/exec.go`,
`pkg/agent/client.go`, `pkg/agent/pty_linux.go`, `pkg/agent/pty_other.go`,
`pkg/agent/*_test.go`.

**Interfaces (exact).**

```go
const (
    Version      = "1"
    DefaultPort  uint32 = 52
    GuestCID     = 3
    MaxFrameSize = 1 << 20
)

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

type Message struct {
    Type    string `json:"type"` // hello|stdout|stderr|exit|file|ok|error
    Version string `json:"version,omitempty"`
    Data    []byte `json:"data,omitempty"`
    Code    *int   `json:"code,omitempty"`
    Error   string `json:"error,omitempty"`
}

func WriteFrame(w io.Writer, v any) error
func ReadFrame(r io.Reader, v any) error
func ServeConn(rw io.ReadWriteCloser) error

type Client struct { /* unexported fields */ }
func Dial(ctx context.Context, udsPath string, port uint32) (*Client, error)

type ExecOptions struct {
    TTY            bool
    Stdin          io.Reader
    Stdout, Stderr io.Writer
    Env            []string
    WorkDir        string
}
func (c *Client) Exec(ctx context.Context, cmd []string, opts ExecOptions) (int, error)
func (c *Client) CopyTo(ctx context.Context, guestPath string, src io.Reader, mode uint32) error
func (c *Client) CopyFrom(ctx context.Context, guestPath string, dst io.Writer) error
func (c *Client) Close() error
```

**Behaviour.** `Dial` dials the muxer at `<udsPath>` itself (not
`<udsPath>_<port>`), writes `CONNECT <port>\n`, reads `OK <host-side-port>\n`
(any numeric port), then exchanges `hello`; a version mismatch returns a
clear error. `ServeConn` dispatches; an unknown op returns an `error` message.
`Exec` streams `stdout`/`stderr` frames and returns the exit code. `put`/`get`
stream `file` frames then `ok`.

**Verification.** `go test -short -race ./pkg/agent/`

**Depends on.** none.

### T2 — `cmd/sc-agent`: agent binary + AF_VSOCK listener

**Goal.** A standalone linux/amd64 binary that listens on AF_VSOCK and serves
`pkg/agent`.

**Files.** `cmd/sc-agent/main.go`, `cmd/sc-agent/vsock_linux.go`,
`cmd/sc-agent/vsock_other.go`, `cmd/sc-agent/main_test.go`.

**Interfaces.** `func Listen(port uint32) (net.Listener, error)` over AF_VSOCK
(`VMADDR_CID_ANY`). `main`: flags `-port` (default `agent.DefaultPort`) and
`-version`; accept loop → `agent.ServeConn`.

**Verification.** `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/sc-agent`
and `go test -short ./cmd/sc-agent/`.

**Depends on.** T1.

### T3 — `pkg/image`: embed, inject, auto-start the agent

**Goal.** Ship the agent in every rootfs and start it from the init wrapper.

**Files.** `pkg/image/embedded_binaries.go`, `pkg/image/init.go`,
`pkg/image/wrapper.go`, `pkg/image/preparer.go` (bump `guestOverrideVersion`
`"2"` → `"3"`), `pkg/image/binaries/sc-agent-amd64` (built from T2), `Makefile`,
`pkg/image/*_test.go`.

**Behaviour.** Write `/sbin/sc-agent` (mode 0755) from the embedded bytes in both
init paths; the init wrapper starts it in the background (briefly waiting for
`/dev/vsock`) before the final `exec`. Bump `guestOverrideVersion` `"2"` → `"3"`
**and** make `guestOverrideReady(rootfsPath)` an unconditional part of the cached
rootfs validity check in `preparer.go` — otherwise a plain service (no
`--hostname`/`--dns`) reuses a stale image and the guest never gets the agent.

**Verification.** `go test -short -race -run 'Init|Wrapper|Agent' ./pkg/image/`

**Depends on.** T2.

### T4 — Firecracker vsock device (daemon + CLI)

**Goal.** Both VM paths configure a vsock device.

**Files.** `pkg/swarmkit/translator.go`, `pkg/swarmkit/vmm.go`,
`pkg/translator/translator.go`, `pkg/lifecycle/vmm.go`, tests.

**Behaviour.** The translator emits
`"vsock": {"guest_cid": 3, "uds_path": "<socketDir>/<task-id>.vsock"}`;
`configureVM` in both paths issues `PUT /vsock` before `/actions`.

**Verification.** `go test -short -race ./pkg/swarmkit/ ./pkg/translator/ ./pkg/lifecycle/`

**Depends on.** none.

### T5 — CLI `vm exec` + `vm cp`

**Goal.** The user-facing commands.

**Files.** `cmd/swarmcracker/cmd_vm_exec.go`, `cmd/swarmcracker/cmd_vm_cp.go`,
`cmd/swarmcracker/cmd_vm.go`, tests.

**Behaviour.** `vm exec [-it] [-e K=V] [-w DIR] <vm> -- <cmd...>`;
`vm cp <src> <vm>:<dst>` / `vm cp <vm>:<src> <dst>`; resolve the VM via
`resolveLiveVMTaskID`; connect via `agent.Dial`; `-it` sets host raw mode and
requests a guest TTY.

**Verification.** `go test -short -race ./cmd/swarmcracker/`

**Depends on.** T1.

### T6 — Docs

**Files.** `docs-site/src/content/docs/reference/cli.md`, plus a guide page.

**Verification.** `cd docs-site && npm run verify`

**Depends on.** T5.

### Controller e2e (not a subagent task)

On the 2-node lab: build + deploy, then `vm exec` echo / exit code / stderr,
`-it`, `vm cp` both directions, and the version-mismatch error. Followed by the
final whole-branch review.

## Serialisation

All tasks run serially (T1 → T2 → T3 → T4 → T5 → T6): T2/T3 share
`cmd/sc-agent` / `pkg/image`; T4 touches the translators; T5 touches
`cmd/swarmcracker`; T6 follows T5.

## Risks / rulings

- **Fixed `GuestCID = 3` for all VMs.** Each VM is its own Firecracker process
  with its own UDS, so CIDs need not be host-unique. Verify in e2e; fall back to
  a per-task CID if Firecracker rejects it.
- **PTY without a dependency.** Use `unix.TIOCSPTLCK` / `unix.TIOCGPTN` +
  `unix.IoctlSetWinsize`. Fallback if flaky: add `github.com/creack/pty`
  (requires `go mod tidy`).
- **Images that keep their own init** (OpenRC/sysvinit) still get the binary
  injected; T3 covers the wrapper path and documents the remainder.
