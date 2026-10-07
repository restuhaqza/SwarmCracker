# Spec: SwarmCracker CLI Fixes (Audit 2026-10-07)

Status: **Draft — awaiting approval**
Branch: `fix/cli-audit-fixes` (off `origin/main` @ `81ff21c`)
Source findings: Obsidian note *SwarmCracker CLI Audit 2026-10-07* (3 High, 5 Medium, 13 Low)

---

## Objective

Fix **all 21 findings** from the CLI audit so the documented CLI workflows
actually work, then ship them as one PR. The audit found three broken control
paths (`vm stop`, `service scale … 0`, `vm snapshot restore`) plus a set of
usability/consistency defects.

- **User:** operators running the `swarmcracker` CLI on a node (single- and
  multi-node clusters) and scripts that parse its output.
- **Success looks like:** every module's acceptance criteria pass on the live
  test node (`root@192.168.18.25`, Firecracker v1.15.1) and in unit tests, with
  no regressions and updated docs.

## Capability Map

| Module id | Responsibility | Findings | Depends on |
|---|---|---|---|
| `vm-lifecycle` | Record PID; make `vm stop`/`status`/`list` correct | BUG-01 | — |
| `service-scaling` | Scale-to-zero + explicit-flag replica updates | BUG-02 | — |
| `snapshot-restore` | Restore via `/snapshot/load`; register restored VM | BUG-15 | `vm-lifecycle` (PID) |
| `id-resolution` | Prefix/name resolution; attach liveness | BUG-03, BUG-16 | — |
| `observability` | `metrics` sees daemon VMs; un-deprecate | BUG-04 | `id-resolution` |
| `setup-config` | `config validate <path>`; `setup check` rootfs | BUG-06, BUG-07 | — |
| `network-introspection` | Real VXLAN/bridge output or labeled experimental | BUG-05 | — |
| `ux-polish` | Help/deprecation/hints/flags/cleanup | BUG-08–14, 17–21 | all above |

Build order: `vm-lifecycle` → `snapshot-restore`; `service-scaling`,
`id-resolution` → `observability`; `setup-config`, `network-introspection`;
`ux-polish` last (touches the same files as everything else).

## Tech Stack

Go 1.26 · cobra · SwarmKit v2 (`github.com/moby/swarmkit/v2/api`) ·
Firecracker v1.15.1 (min v1.14) · zerolog · go-containerregistry ·
`text/tabwriter` · standard `testing`.

## Commands

```bash
# Build
make swarmcracker && make all

# Test (host)
go test ./pkg/... ./cmd/...        # or: make test
go test -v ./pkg/runtime/          # one package

# Linux-only packages (pkg/network, pkg/image) can't build on macOS:
GOOS=linux GOARCH=amd64 go build ./...
GOOS=linux GOARCH=amd64 go vet ./...

# Format / lint
make fmt && make lint

# Docs (only when docs-site changes)
cd docs-site && npm run verify

# Live verification (test node)
GOOS=linux GOARCH=amd64 go build -o /tmp/sc ./cmd/swarmcracker
scp /tmp/sc root@192.168.18.25:/tmp/sc && ssh root@192.168.18.25 /tmp/sc ...
```

## Project Structure

```
cmd/swarmcracker/   → cobra commands (one new<X>Command per command)
pkg/runtime/        → VM state, discovery, PID resolution
pkg/lifecycle/      → CLI VM lifecycle (Firecracker process)
pkg/swarmkit/       → daemon-side VMM/executor (service tasks)
pkg/snapshot/       → snapshot create/restore/list/delete
pkg/console/        → VM serial-console channel + resolution
test/mocks/         → shared mocks
docs-site/src/content/docs/ → published docs (Astro Starlight)
docs/               → internal material (this spec/plan/tasks)
```

## Code Style

One command constructor per file; resolution through shared helpers; return
errors (don't print `Usage` on runtime errors); JSON via `encoding/json`;
table output via `text/tabwriter`.

```go
// Resolve a service ref (full id, unique id prefix, or name) to its full ID.
func resolveServiceID(ctx context.Context, client api.ControlClient, ref string) (string, error) {
	services, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list services: %w", err)
	}
	return matchRef(ref, services); // exact id > unique prefix > exact name; else error
}
```

## Testing Strategy

- **Unit tests** per changed package, table-driven: reference resolvers, PID
  resolution (by `--id` and by `--api-sock`), scale/update semantics, snapshot
  restore request flow, `config validate` path handling, `setup check` rootfs
  scan, network output/JSON.
- **Cross-compile** `GOOS=linux go build ./...` for Linux-only code.
- **Live verification** on `192.168.18.25` per module acceptance criteria;
  clean up every artifact afterwards.
- **Docs:** update `docs-site/.../reference/cli.md` and relevant guides for any
  flag/behavior change; run `npm run verify`.

## Boundaries

- **Always:** `make fmt`; run affected `go test`; cross-compile; keep errors
  actionable; update docs for behavior/flag changes; clean up live-server
  artifacts.
- **Ask first:** new SwarmKit API calls beyond reference resolution; adding or
  removing commands; changing exit codes; any daemon-side (`pkg/swarmkit`)
  behavior change that affects service tasks.
- **Never:** commit secrets; leave the test cluster worse than found;
  remove/disable existing tests; make unrelated changes.

---

## Module requirements

### M1 — `vm-lifecycle` (BUG-01)

**Requirement.** `vm create -d` records the real Firecracker PID; `vm stop`
actually terminates a CLI-created VM and cleans its sockets; `vm status`/`vm
list` report accurate PID and state.

**Approach.**
1. Launch CLI Firecracker with `--id <taskID>` in `pkg/lifecycle/vmm.go` so
   `/proc` matching works (consistent with the daemon path).
2. Extend `runtime.firecrackerTaskID`/`FindFirecrackerPIDs` to also match
   `--api-sock <dir>/<id>.sock` (covers processes launched without `--id`).
3. In `cmd_vm.go` after `Start`, set `vmState.PID` from
   `runtime.FindFirecrackerPID(task.ID)` (fallback: socket-based lookup).
4. In `cmd_stop.go`, when `state.PID <= 0`, re-resolve by ID/socket; if the
   socket is not alive, report "already stopped" and clean state; otherwise
   signal, then remove the API + console sockets and set status `stopped`.

**Acceptance.**
- `vm create --golden … -d` → `vm list`/`vm status` show a non-zero PID.
- `vm stop <id>` → process gone, `.sock`/`.console.sock` gone, `vm list` no
  longer shows it; `vm list --all` shows it `stopped`.
- `vm stop` still refuses daemon/service VMs with the existing guidance.

**Files.** `pkg/lifecycle/vmm.go`, `pkg/runtime/discovery.go`,
`cmd/swarmcracker/cmd_vm.go`, `cmd/swarmcracker/cmd_stop.go`,
`cmd/swarmcracker/vm_lookup.go`, tests.

### M2 — `service-scaling` (BUG-02)

**Requirement.** Scaling to zero works; `service update --replicas N` applies
when the flag is passed (including `0`) and is a no-op when omitted.

**Approach.** Use `cmd.Flags().Changed("replicas")` so `updateService` applies
an explicit `0`; `scaleService` always applies the parsed value. Reject negative
counts with a clear message (accept `-- -1`? no — error).

**Acceptance.**
- `service scale <svc> 0` → replicas 0, running tasks stop.
- `service scale <svc> 3` → replicas 3.
- `service update <svc> --replicas 0` → replicas 0; omitting `--replicas` leaves
  the count unchanged.
- `service scale <svc> -1` → clear invalid-count error.

**Files.** `cmd/swarmcracker/cmd_service.go`, tests.

### M3 — `snapshot-restore` (BUG-15)

**Requirement.** `vm snapshot restore` boots a working VM from a snapshot on
Firecracker v1.15.1, and the restored VM is registered for management.

**Approach.** Start Firecracker with **only** `--api-sock <sock>` (no
`--snapshot`), then `PUT /snapshot/load` with `snapshot_path`, `mem_file_path`,
`resume_vm: true`. Ensure the snapshot records the rootfs path and that it
exists at restore time; return a clear error otherwise. On success, add a CLI
state entry (id `<task>-restored`, PID, socket, `running`, `LogPath`).

**Acceptance.**
- Create a snapshot of a golden VM, kill the source, `vm snapshot restore
  <snap>` → new Firecracker process alive, API socket answers, `vm list` shows
  the restored VM, `vm stop <restored>` stops it.
- Restore of a snapshot whose rootfs is missing → actionable error.
- `vm snapshot` help/examples reference `vm snapshot …` (see M8).

**Files.** `pkg/snapshot/snapshot.go`, `cmd/swarmcracker/cmd_snapshot.go`,
tests (unit) + live verification.

### M4 — `id-resolution` (BUG-03, BUG-16)

**Requirement.** IDs shown in tables are usable; ambiguous refs error; `vm
attach` only resolves live VMs.

**Approach.** Add shared resolvers that accept exact full ID, unique ID prefix,
or (services) name, used by: `service inspect|ps|scale|update|rm`,
`node inspect|drain|activate|promote|rm`, `task inspect`. `service ps` on an
unknown ref returns an error (not "No tasks found"). `vm attach` resolves via
`runtime.DiscoverRunningVMs` (liveness) and maps to the console socket, instead
of listing `*.console.sock` files.

**Acceptance.**
- `node inspect <12-char>` / `service inspect <12-char>` / `service scale
  <12-char> 2` / `service ps <12-char>` all succeed.
- Ambiguous prefix → error listing candidates.
- `vm attach <dead-task-id>` → "does not match any running VM"; `vm attach
  <live-prefix>` works.

**Files.** `cmd/swarmcracker/cmd_node.go`, `cmd_service.go`, `cmd_task.go`,
`cmd_vm_attach.go`, `pkg/console/resolve.go`, `cmd/swarmcracker/vm_lookup.go`,
tests.

### M5 — `observability` (BUG-04)

**Requirement.** `metrics` reports daemon/service VMs; it is a normal command.

**Approach.** Reuse `DiscoverRunningVMs` + `MergeVMs` + `enrichVMs` in
`cmd_metrics.go`; register `newMetricsCommand()` at top level in `main.go` and
remove the deprecated wrapper + the bogus `cluster status --metrics` hint.

**Acceptance.** With a service VM running, `metrics` shows a row for it and
`metrics --format json` emits JSON; no deprecation banner; `metrics` and
`vm list` agree.

**Files.** `cmd/swarmcracker/main.go`, `cmd_metrics.go`, `cmd_deprecated.go`,
tests.

### M6 — `setup-config` (BUG-06, BUG-07)

**Requirement.** `config validate [path]` validates the given file; `setup
check` detects any available rootfs and returns a real failure count.

**Approach.** Give `config validate` an optional positional path (used if
provided, else `-c`/default); `setup check` scans the configured rootfs dir for
`*.ext4` plus the known `bionic.rootfs.ext4`; `countFailures` returns the actual
number of failures.

**Acceptance.**
- `config validate /nonexistent.yaml` → error (file not found), not "valid".
- `config validate <good file>` validates that file.
- `setup check` passes `rootfs` when `ubuntu-24.04.ext4`/`alpine-latest.ext4`
  exist; exit code reflects the real number of failures.

**Files.** `cmd/swarmcracker/cmd_config.go`, `cmd_setup.go`, tests.

### M7 — `network-introspection` (BUG-05)

**Requirement.** `network vxlan ls|status` and `network bridge status` return
real data (or are explicitly labeled experimental) and honor `--format json`.

**Approach.** Implement read-only introspection: bridge state/addresses/TAPs via
`ip`/`bridge`; VXLAN interfaces via `ip -d link show type vxlan` and
`bridge fdb`. If a sub-command cannot be fully implemented, mark it
`[experimental]` in help/output rather than printing the doctor stub. Honor
`--format json`.

**Acceptance.**
- `network bridge status` prints bridge name/state/addresses and members.
- `network vxlan ls` lists VXLAN interfaces/peers (or states none found).
- `--format json` produces parseable JSON for both.

**Files.** `cmd/swarmcracker/cmd_network.go` (+ `pkg/network` if reusable),
tests.

### M8 — `ux-polish` (BUG-08,09,10,11,12,13,14,17,18,19,20,21)

**Requirement.** Consistency/doc fixes:
- **08** `vm ls` alias for `vm list`.
- **09** deprecation banner lists only real commands.
- **10** `cluster status` (no arg) prints a cluster summary; `cluster status
  <vm-id>` keeps returning VM status.
- **11** `vm snapshot` help/examples use `vm snapshot …`.
- **12** guard `node.ID[:12]` truncation.
- **13** reject invalid `vm list --format`.
- **14** `asset kernel verify` reports "is a directory" for dirs.
- **17** `node drain` on the sole manager prints a warning.
- **18** `init`/`join` success text uses non-deprecated commands.
- **19** `node promote` on an existing manager reports "already a manager".
- **20** clear invalid-replica error for `service scale -1`.
- **21** remove stale API/console sockets for CLI-managed VMs on stop; daemon
  task teardown removes its sockets (verify, fix in `pkg/swarmkit/vmm.go` if
  missing).

**Acceptance.** Each bullet verified live and/or by unit test; `--help` output
matches reality.

**Files.** `cmd/swarmcracker/cmd_list.go`, `cmd_deprecated.go`, `cmd_cluster.go`,
`cmd_snapshot.go`, `cmd_node.go`, `cmd_asset.go`, `cmd_init.go`, `cmd_join.go`,
`cmd_status.go`, `cmd_service.go`, `pkg/swarmkit/vmm.go`, tests.

---

## Assumptions

1. Target the installed Firecracker v1.15.1 (min supported v1.14).
2. The PR bases on `origin/main` (@`81ff21c`) and is independent of PR #31
   (`fix/vm-logs`); rebase if #31 lands first.
3. `service update --replicas` uses flag-set detection (explicit `0` applies).
4. `cluster status` keeps `<vm-id>` back-compat and adds a no-arg summary.
5. `metrics` is un-deprecated as a top-level command.
6. Restored VMs are registered in CLI state.
7. Network introspection is best-effort read-only; unfeasible parts are labeled
   `[experimental]`.
8. All 21 findings are in scope; verification is live + unit tests.

## Open Questions

None outstanding — the four design decisions above were confirmed. New questions
discovered during implementation will be recorded here and raised before
changing scope.
