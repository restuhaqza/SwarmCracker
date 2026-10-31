---
title: Guest Access
description: Run commands and copy files inside a running microVM with vm exec and vm cp over a vsock guest agent.
---

A microVM is a black box: its workload runs behind its own kernel, so there is
no shared filesystem and no host process to `exec` into. SwarmCracker closes that
gap with a small **guest agent** reachable over a vsock control channel. The
`swarmcracker vm exec` and `swarmcracker vm cp` commands use it to run a command
in the guest or move a file in and out — no SSH server, and **no extra host port
opened**.

```bash
# Run a command and get its exit code, stdout, and stderr back
swarmcracker vm exec 5f3a1b2c9d -- uname -a

# Open an interactive shell
swarmcracker vm exec -it myvm -- /bin/sh

# Copy a file in and out
swarmcracker vm cp ./config.yaml myvm:/etc/app/config.yaml
swarmcracker vm cp myvm:/var/log/app.log ./app.log
```

`<vm>` is a task ID, or any unique prefix of one (the 12-character ID shown by
`swarmcracker task ls`) — the same resolution `vm attach` uses.

## How it works

A small guest agent, `sc-agent`, is injected into the image at `/sbin/sc-agent`
and started by the init wrapper at boot. It listens on **vsock port 52**.

The host reaches it through Firecracker's vsock device. It connects to the
muxer socket `<socket-dir>/<task-id>.vsock` (default socket directory
`/var/run/firecracker`) and sends `CONNECT 52` to ask Firecracker to attach the
connection to guest vsock port 52. There is no `<task-id>.vsock_52` file in this
host-to-guest direction: that `<socket>_<port>` path is created only for
*guest-initiated* connections. The traffic never leaves the host — **no host
network port is opened** and there is no listening TCP socket in the guest.

The client checks the agent's protocol version during the handshake on connect
and **fails clearly on a mismatch**, rather than mis-parsing a reply from an
older or newer agent.

## `vm exec`

Runs a command inside a running microVM and returns the guest's exit code,
stdout, and stderr. The guest command's exit code becomes the exit code of
`swarmcracker vm exec`.

```bash
swarmcracker vm exec <vm> -- <command> [args...]
```

Everything after `--` is passed to the guest unchanged.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--socket-dir` | — | `/var/run/firecracker` | Directory containing microVM vsock sockets |
| `--state-dir` | — | `$SWARM_STATE_DIR` or `/var/lib/swarmkit` | The daemon's state directory; it holds the restored-instance record that names a migrated task's vsock muxer |
| `--interactive` | `-i` | `false` | Keep stdin open (requires `-t`) |
| `--tty` | `-t` | `false` | Allocate a pseudo-terminal in the guest |
| `--env` | `-e` | — | Set an environment variable in the guest (repeatable, `K=V`) |
| `--workdir` | `-w` | — | Working directory in the guest |

```bash
# Simple command
swarmcracker vm exec 5f3a1b2c9d -- uname -a

# Environment and working directory
swarmcracker vm exec -e APP=prod -e LOG_LEVEL=debug -w /srv myvm -- env

# Interactive TTY (combine the two short flags)
swarmcracker vm exec -it myvm -- /bin/sh
```

`-i` only has an effect with `-t`: the protocol carries no stdin channel for a
non-TTY command, so without `-t` the command runs with its stdin closed. The CLI
prints a warning if `-i` is passed without `-t`.

## `vm cp`

Copies a file between the host and a running microVM. Exactly one of the two
arguments carries the `<vm>:` prefix, which selects the direction:

```bash
# host -> guest
swarmcracker vm cp <src> <vm>:<dst>

# guest -> host
swarmcracker vm cp <vm>:<src> <dst>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--socket-dir` | `/var/run/firecracker` | Directory containing microVM vsock sockets |
| `--state-dir` | `$SWARM_STATE_DIR` or `/var/lib/swarmkit` | The daemon's state directory; a migrated task's muxer is read from its restored-instance record there |
| `--mode` | `0644` | File mode for host-to-guest copies (octal) |

```bash
swarmcracker vm cp ./config.yaml 5f3a1b2c9d:/etc/app/config.yaml
swarmcracker vm cp --mode 0600 ./secret myvm:/root/secret
swarmcracker vm cp myvm:/var/log/app.log ./app.log
```

The first `:` in an argument selects the direction, so a local path may not
contain a colon.

## Trusted control path

The vsock control channel lets a **host-local process run commands in the guest
as root**, and read and write guest files. Treat it with the same trust as the
**Firecracker API socket**: anything that can reach the socket can fully control
the VM.

Access is limited to the host's socket directory (`/var/run/firecracker`,
owned by the daemon), so run `vm exec` / `vm cp` as root or with `sudo`, as you
would with `vm attach`. This is a trusted control path, not a tenant-facing
interface — see [Security & Jailer](/guides/security/) for the host isolation
model.

## Limitations

1. **Files only for `vm cp`.** Directories are not supported yet; copying a
   directory is rejected with a clear error.
2. **A TTY exec is one-shot.** Raw stdin shares the same connection as the
   framed requests, so the connection is closed after a TTY command and each
   `vm exec` opens a fresh connection. A non-TTY exec does not accept stdin.
3. **Images with their own init do not auto-start the agent.** Images that keep
   an init such as OpenRC or sysvinit get the agent binary injected at
   `/sbin/sc-agent` but do not start it automatically yet. Images on the
   tini-wrapper path (scratch, Alpine, and other no-init images) start it for
   you.
4. **Jailer mode is not supported yet.** VMs started with the Firecracker
   jailer do not get a vsock device, so `vm exec` and `vm cp` are unavailable
   for them.
5. **Prebuilt golden images have no agent.** Golden images are built outside the
   rootfs preparer, so they contain no `/sbin/sc-agent`; `vm exec` and `vm cp`
   cannot work on a VM booted from a `--golden` image.

## Troubleshooting

### `dial vsock socket ...: no such file or directory`

The VM has no vsock device, or the socket directory does not match the
daemon's:

- The VM was started with the jailer (not supported yet — see above).
- `--socket-dir` does not match the daemon's `--socket-dir`; both default to
  `/var/run/firecracker`.
- The task was moved here by `vm migrate`. Its VM answers on the **source**
  task's muxer, so the path is read from
  `<state-dir>/restored-instances/<task>.json`; if the file is missing, or
  `--state-dir` does not match the daemon's, the command falls back to
  `<task>.vsock` and reports exactly this error. See
  [Live Migration](/guides/migration/).

### The agent does not respond

- The image keeps its own init system, so the agent was injected but never
  started (see limitation 3).
- The VM is not running; check with `swarmcracker vm list`.

## Related

- [CLI Reference](/reference/cli/) — `vm exec` and `vm cp` flags
- [Security & Jailer](/guides/security/) — host isolation and the trust model
- [Operations](/guides/operations/) — `vm attach`, logs, and lifecycle
