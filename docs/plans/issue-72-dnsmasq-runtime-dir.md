# Plan — #72: dnsmasq runtime files must not sit at predictable `/tmp` paths

Branch: `fix/dnsmasq-runtime-dir`
Issue: [#72](https://github.com/restuhaqza/SwarmCracker/issues/72)

## Problem

Four per-bridge `dnsmasq` files live at predictable paths in a world-writable
`/tmp`, and the daemon runs as root with no `PrivateTmp` (the shipped units removed
their hardening on purpose: `# ReadWritePaths removed to avoid mount namespace
issues`). That combination gives a local unprivileged user two ways to reach
root-owned resources:

1. **Root file overwrite.** `os.WriteFile` follows symlinks. Pre-creating
   `/tmp/dnsmasq-swarm-br0.hosts` as a symlink to any path gets that path
   overwritten as root on the next reconcile (and the log file at every start).
2. **Arbitrary root kill.** `cleanupDnsmasq` globs `/tmp/dnsmasq-*.pid`, reads
   whatever PID is inside and `kill`s it as root. A planted file makes the daemon
   SIGTERM any process — including a root one — and `liveDnsmasqPID` reads the same
   file, so a planted live PID also makes the daemon believe a server it did not
   start is healthy and skip starting its own.

## What the recon established (on the 2-node lab, dnsmasq 2.91)

| Fact | Evidence |
|------|----------|
| With no `--user` flag dnsmasq drops to **nobody** (`uid=65534`, `gid=nogroup`) | `ps -o user= -p $(cat /tmp/dq.pid)` → `nobody` |
| dnsmasq creates its pid file and log file **as root** at startup, then chowns them to nobody | `-rw-r--r-- 1 nobody nogroup … x.pid`, `-rw-rw---- 1 nobody root … x.log` |
| The **lease file is created lazily**, i.e. after the privilege drop | no lease file existed after startup; `su nobody -c 'echo x > <lease>'` in a root-owned `0755` dir → **Permission denied** |
| A dir owned `root:<dnsmasq's group>` mode `0770` is writable by dnsmasq and by nobody else | `new.leases` created as `nobody:nogroup`; `su daemon -c 'touch x'` → Permission denied |
| `/run` is a root-owned tmpfs and `/run/swarmcracker` already exists (the CLI's state dir) | `ls -ld /run /run/swarmcracker`, `mount \| grep ' /run '` |

So the runtime directory must be **root-owned, group = the group dnsmasq drops to,
mode `0770`**: dnsmasq can create and rewrite everything it needs after the drop,
and no other local user can create, replace or even list anything in it.

## Design

- **Location.** `<runtime-dir>/dnsmasq-<bridge>.{hosts,pid,log,leases}`, with the
  runtime directory defaulting to `/var/lib/swarmcracker/dnsmasq` — the project's
  state directory, which the shipped unit lists in `ReadWritePaths`. One helper per
  file, so no path is spelled twice.

  The lab e2e chose the location, not the issue's suggestion: the shipped unit runs
  with `ProtectSystem=strict` and `PrivateTmp=true`, so `/run` is read-only inside
  it and `mkdir /run/swarmcracker/dnsmasq` fails with `read-only file system`
  (`/var/lib/firecracker` is read-only there too; only the listed paths are
  writable).
- **Creation.** The daemon creates the directory before writing anything: it
  refuses a symlink or non-directory at that path, `MkdirAll`s it `0770`, and hands
  it to dnsmasq's group (`nobody`'s gid) with `Chown`/`Chmod`. When the lookup or
  the chown fails (non-root, exotic image), it falls back to `0755` root:root and
  says so — serving still works, only the lease rewrite degrades.
- **Writes.** The daemon's writes to these files go through a `O_NOFOLLOW` writer,
  so even a symlink that somehow appeared cannot redirect a root write.
- **Signals.** No PID read from a file is signalled until it is proven to be a
  dnsmasq bound to that bridge, using the `/proc` argv matcher the reuse path
  already relies on (`--interface <bridge>`). `liveDnsmasqPID` applies the same
  check, so a planted PID cannot masquerade as a healthy server either.
- **Upgrades.** `cleanupDnsmasq` still sweeps the legacy `/tmp/dnsmasq-*.pid`
  glob so an instance started by the previous release is reaped — now through the
  validated kill, so a planted file there is inert.
- The `PrivateTmp` note in the code and the comment that justified the `/proc`
  fallback become obsolete; they are updated rather than left to mislead.

## Explicitly out of scope

- Adding `PrivateTmp=true` to the units. With the files out of `/tmp` it buys
  nothing here, and it changes what a debugging session can see.
- The daemon's *other* use of `/tmp` (snapshot staging, log paths), if any.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | Runtime dir + per-file path helpers + `O_NOFOLLOW` writer + group handover | `pkg/network/dns.go` | `go test ./pkg/network/` |
| T2 | dnsmasq args, pid/log/hosts creation, `liveDnsmasqPID`, `killDnsmasqByPID`, `cleanupDnsmasq` (incl. legacy sweep), validated kill | `pkg/network/manager.go` | `go test ./pkg/network/` |
| T3 | Tests: planted symlink cannot redirect a write; planted pid file cannot signal a non-dnsmasq process nor look healthy; files live outside `/tmp`; existing dnsmasq tests point at a temp runtime dir | `pkg/network/*_test.go` | `go test ./pkg/network/` |
| T4 | Docs: where the runtime files live | `guides/networking.md`, `guides/operations.md` | `npm run verify` |
| T5 | Independent whole-branch review | — | — |
| T6 | Lab e2e: service DNS still resolves, dnsmasq reuse works, no `/tmp` files, dir ownership, cleanup | — | see below |

## Verification

- Unit: a symlink planted at the records/log path is not followed (the target is
  untouched); a pid file naming a live non-dnsmasq process neither gets it
  signalled nor makes `liveDnsmasqPID` report a server; the files are created under
  the runtime directory, never in `/tmp`.
- Lab e2e: a service's name resolves inside a guest (the #42 path still works
  end to end), `setupDHCP` reuses the running instance instead of bouncing it, the
  four files live in the runtime directory owned `root:nogroup` `0770`, no
  `dnsmasq-*` file remains in `/tmp`, and a planted pid file naming a live
  non-dnsmasq process is neither signalled nor treated as a server. Note that
  dnsmasq (and its pid file) normally outlives `service rm` — `cleanupDnsmasq` runs
  at daemon shutdown — so the pid file is expected to be gone after a restart, not
  after a service removal.
