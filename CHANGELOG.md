# Changelog

All notable changes to SwarmCracker will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Optional OpenTelemetry tracing** — `swarmd-firecracker --tracing-enabled
  --tracing-endpoint http://collector:4318` exports one OTLP/HTTP trace per task
  covering image preparation, VM boot, the running lifetime and teardown, with
  `task.id`, `service.id`, `node.id` and `image.ref` attributes (issue #91). It is
  off by default: a disabled node constructs no exporter and dials nothing. The
  executor only ever sees a two-method interface, so the OpenTelemetry SDK stays
  behind one seam and disabling tracing removes it without touching a call site.
  Spans are per node — cross-node propagation over the manager's API is not
  implemented yet — and metrics/logs are unchanged.
- **Cross-node logs: `vm logs --host` and `service logs`** — a task's console log
  can now be read from any machine holding an API token: `-H` sends the request to
  the manager, which relays it to the node that owns the task, and the new
  `service logs <service>` merges every replica's output into one interleaved
  stream with a short task-ID prefix (issue #89). Aggregation is a query-time
  fan-out, not a collector, so a single-node install keeps reading files directly
  and needs no extra component.
- **Per-task resource metrics from inside the guest** — the daemon asks the guest
  agent for the VM's own `/proc` counters every 15 seconds and publishes
  `swarmcracker_vm_cpu_seconds`, `vm_memory_bytes`, `vm_net_rx_bytes` and
  `vm_net_tx_bytes` with `instance`/`task_id`/`service` labels (issue #88). These
  four series existed but had no writer, so per-VM CPU, memory and network use
  could only be guessed from the Firecracker process. A failed sample publishes
  nothing rather than a zero, and teardown deletes the task's series.
- **Service autoscaling on guest CPU** — `service create|update --autoscale
  min=2,max=10,metric=cpu,target=70[,cooldown-up=60s][,cooldown-down=5m]` keeps a
  service's replica count between two bounds by scaling on the CPU its workload
  burns inside its own microVM (issue #49). The policy is stored as six
  `swarmcracker.autoscale.*` service labels, so it survives an update that does
  not mention it and `service inspect` prints it back; `--autoscale ""` clears
  it. The manager's leader samples every running replica every 30 seconds,
  converts successive cumulative guest readings into cores per replica, and
  moves the count by one at a time: up on the first sample above
  `target × 1.1`, down only after two consecutive samples below
  `target × 0.9` and its cooldown, never outside `[min,max]` or below 1. A
  service at 0 replicas counts as paused and is left alone. On the manager,
  `swarmcracker_service_replicas` reports the count the last decision settled on
  and `swarmcracker_service_scale_events_total` counts the changes actually
  applied.
- **`service ps` and `task ls` explain a refused placement** — the `ERROR` column
  carries SwarmKit's verdict, which is generic ("scheduling constraints not
  satisfied on 2 nodes") and truncated at 40 characters, so an operator had to
  guess which constraint key was missing. When no task of the listing is placed,
  the table is now followed by one diagnostic line. It names the constraint only
  when the task error carries the constraint filter's own explanation; otherwise
  it quotes the scheduler verbatim and lists the declared constraints as declared
  rather than as the cause, because SwarmKit joins every failing filter into one
  error and a constraint's presence is not evidence that it failed. The line is a
  diagnostic, not an error: no exit code changes, it prints in table mode only,
  terminal rows do not suppress it, and `task ls --health` does not print it.

### Changed
- **`service ls` shows the service mode** — the table gains a `MODE` column
  (`replicated` / `global`), so a global service is visible without inferring it
  from the `REPLICAS` cell.

### Fixed
- **Snapshots of a daemon-managed task work on a systemd-managed node** —
  `vm snapshot create` on a service replica failed with `Read-only file system`
  even though the pause succeeded. The units run with `ProtectSystem=strict` and
  Firecracker inherits the daemon's mount namespace, but the snapshot directory
  was not in `ReadWritePaths` and the units did not pass `--snapshot-dir` at all.
  `cluster init` and `cluster join` now take `--snapshot-dir` (default
  `/var/lib/firecracker/snapshots`), pass it to the daemon, pre-create it, and
  list it in `ReadWritePaths` with the `-` prefix so a missing directory cannot
  fail the unit with `status=226/NAMESPACE`. Because the template is now rendered
  by a testable helper, the regression is covered by unit tests rather than only
  on a node.
- **An out-of-bounds replica count is refused, not stored** — an *explicit*
  `service create --replicas`, `service update --replicas` or `service scale`
  now rejects a count outside the service's autoscale `min`/`max` and names both
  bounds, instead of storing a service the manager loop would have to correct on
  its first pass. A `--replicas`-less create is not an explicit count, so it
  still stores the default 1 and the first pass aligns it to `min`.
  `--replicas 0` and `service scale <service> 0` stay valid: zero means paused,
  so nothing is clamped.
- **Console logs are written on a systemd-managed node** — the shipped units run
  with `ProtectSystem=strict` and only made `/run/firecracker` and
  `/run/swarmkit` writable, not `/run/swarmcracker`, the daemon's default log
  directory. Every console log therefore failed to open and `vm logs` had nothing
  to read; the unit templates now create that runtime directory too.
- **A VM restart no longer erases the console log** — the per-task log was opened
  with `O_TRUNC`, so a restart under the same task ID silently threw away the
  output that a post-mortem needs. It now appends and writes a
  `console restarted at ...` boundary line, and a task SwarmKit removes takes its
  log with it, so retention follows the task's lifetime instead of growing
  without bound.
- **`service create --command` / `--args` are applied** — the daemon translator
  never carried the command to the guest, so every service task ran the image's
  own entrypoint while `service inspect` showed the override (issue #97). Each
  argv element now travels as a base64 `sc.argv=` kernel argument and the init
  wrapper execs it in place of the baked OCI command, keeping spaces, quotes and
  empty arguments intact. The guest-override marker is bumped, so cached rootfs
  images rebuild with the new wrapper. Values the kernel command line cannot
  carry (line breaks, NUL) and the `--golden` combination are rejected at create
  time with a clear error instead of being dropped.

## [0.12.0] - 2026-10-26

### Added
- **API contract foundation** — `pkg/api/v1` wire types, `pkg/output` envelope/
  error/exit-code writer, `pkg/client` as the single SwarmKit dialing and
  mapping layer, and `swarmcracker service ls -o json|name` with the v1
  `ServiceList` envelope. Legacy `--format json` still works, but now emits
  `[]` instead of prose on an empty cluster.
- `api/openapi.yaml` with a schema test that validates the service fixtures.
- **Task list v1 output** — `swarmcracker task ls -o json` emits the v1
  `TaskList` envelope (including service/node names, state, image, addresses,
  ports and exit code) and `-o name` emits IDs; legacy `--format json` still
  works, but empty results now emit `[]` instead of prose. Phase 0 omits live
  health from the v1 envelope. Invalid combinations are rejected as usage
  errors: `-o table --format`, `-o ndjson`, `-o json|name --quiet`, and
  `--health-port` in the new modes.
- **Node list v1 output** — `swarmcracker node ls -o json` emits the v1
  `NodeList` envelope (hostname, role, membership, availability, state, address,
  labels, resources, manager status and agent version) and `-o name` emits IDs;
  legacy `--format json` keeps its array shape, but empty and filtered-empty
  results now emit `[]` instead of prose or `null`.
- **Service inspect v1 output** — `swarmcracker service inspect -o json` emits
  the v1 `ServiceDocument` envelope and resolves full IDs, exact names, and
  unique ID prefixes. Legacy `--format json` keeps the raw SwarmKit object and
  its credential redaction.
- **Service ps v1 output** — `swarmcracker service ps -o json` emits the v1
  `TaskList` envelope scoped to one service, and `-o name` emits task IDs.
  Phase 0 omits live health from these modes; `--format json` keeps the legacy
  task array.
- **Config and secret list v1 output** — `config ls -o json` / `secret ls -o json`
  emit the v1 metadata envelopes (`id`, name, revision, timestamps, labels and
  secret driver); `-o name` emits IDs. Payloads are never serialized. Legacy
  `--format json` keeps the existing list shape.
- **Network list v1 output** — `network ls -o json` emits the v1 `NetworkList`
  for user-defined networks (identity, driver, first IPAM pool, labels and
  attachability); `-o name` emits IDs. Legacy `--format json` keeps the raw
  array.
- **API contract acceptance** — `make api-check` runs the envelope, OpenAPI,
  client and command contract tests. The `partial` error code now maps to exit
  status 7, completing the documented exit-code table.
- **Client-boundary guard** — a test now rejects new `api.ControlClient` imports
  in `cmd/swarmcracker` and `pkg/api`, with an explicit allowlist for the legacy
  paths still being migrated.
- **API v1 closure (issue #102)** — the remaining contract items, shipped as one
  campaign:
  - **Live health over the API** — `task ls --health` reads
    `GET /v1/tasks?health=true` through the manager (so it needs
    `-H`/`--host`), prints exactly one merged `TaskList`, and maps a `207`
    partial page to exit status `7`: the page printed, at least one node was
    unreadable.
  - **Interactive attach over the API** — `POST /v1/tasks/{id}/attach` (R18) is
    one HTTP/1.1 full-duplex request: the chunked body is stdin, the chunked
    response is the console, and the broker relays it to the node that runs the
    task. `swarmcracker -H … vm attach <task>` streams it with Ctrl-P Ctrl-Q
    detach; the local socket path is unchanged. New `attach` capability,
    audited once as `task.attach`.
  - **Snapshots are real restore points** — create copies the task disk
    (reflink, else a sparse copy) while the VM stays paused, so memory and disk
    describe the same instant; restore always writes that copy over the recorded
    rootfs path, replacing whatever is there (a stopped restored VM leaves its
    disk behind, and its writes do not belong to this snapshot's memory image).
    A restore of a task that is still running is refused, over the API and from
    the local CLI. A restored VM is stopped explicitly with
    `DELETE /v1/tasks/{id}/restored` or `vm snapshot stop-restored`. Snapshot
    requests the manager relays to a node default to a five-minute deadline
    (`swarmd-firecracker --snapshot-timeout`), and local
    `vm snapshot create` / `restore` take the same five-minute `--timeout`
    instead of a fixed 60-second context.
  - **Query hardening** — each route declares the query parameters it
    implements; an unknown name is `400 invalid` with `details.parameter` instead
    of being silently ignored, and a list cursor is bound to the route and filter
    that produced it (`400 invalid`, `details.reason: "cursor_mismatch"`
    otherwise).
  - **SDK parity** — the Python and Node clients now cover files and the
    experimental snapshot routes, retries are opt-in (honouring `Retry-After`,
    resending an idempotent body exactly), and the generated types are gated by
    a pinned TypeScript compiler over a hostile/accepted fixture.
  - **Internal ingress declared private** — `:4243` (`/v1/ingress`,
    `/v1/ingress/report`) joins `:4242` and `:4245` as an explicitly private,
    unsupported listener (R19/G7). All three bind all interfaces with cluster
    mutual TLS as the only boundary. `:8080` (health and metrics) is in the same
    unsupported set but a different shape: loopback only (`127.0.0.1:8080` by
    default, `--health-addr`) and **no authentication**, so the bind address is
    the boundary.
  - **Daemon image fixed** — `Dockerfile` builds without `make` or a shadowed
    package, produces a static non-PIE daemon, and CI builds and runs it.
  - **Conformance hardened and run for real** — fixtures carry per-run
    identities, the lab target must be `https://`, and a gated case that skips
    fails the run unless `CONFORMANCE_ALLOW_SKIPS=1`. A real run against the lab
    manager (`v0.12.0-lab2`) passed 34 executed subtests; it also found and fixed
    a Firecracker API keep-alive leak that broke a second snapshot create.

### Changed
- `volume export -o` is now `volume export -O` / `--output-file`; the global
  `-o` / `--output` flag is reserved for machine output formats.
- Snapshot operations now have a default deadline (`--snapshot-timeout 5m` on
  `swarmd-firecracker`), so a create relayed through the manager API is no longer
  unbounded; a request past it is `504 timeout` and the node still resumes the VM.
  The local `vm snapshot create` / `restore` commands keep their own 60-second
  context and are unaffected.
- The `:4243` ingress endpoints and the node broker are documented as private:
  they are not part of the v1 contract and may change in any minor release.

### Fixed
- `Dockerfile` no longer depends on `make` at build time or on a package that
  shadowed the daemon binary; the image builds and runs in CI.
- Snapshot create no longer leaks keep-alive connections to the Firecracker API
  socket, which made a second create fail `503 Too many open connections` and
  left the VM paused.
- The API now refuses a redirect in the document, stream and attach clients
  instead of following it (a redirected request could replay the bearer token
  and return HTML as console or file bytes).

---

## [0.11.0] - 2026-10-19

### Added
- **Private-registry authentication** — `service create --with-registry-auth`
  ships the manager's registry credentials in the service spec
  (`PullOptions.RegistryAuth`), so workers pull private images; the credential
  is masked from task output (`pkg/redact`), and `--with-registry-auth=false`
  clears it on update.
- **Settable node labels** — `swarmcracker node update --label-add KEY=VALUE`
  (repeatable, overwrites) and `--label-rm KEY` (removing a missing key is a
  no-op), so placement constraints are managed without editing state.
- **Unschedulable reasons** — a task that cannot be placed now reports the
  reason (constraint, volume, …) instead of a bare PENDING.
- **Memory balloon** — per-service `--balloon-mib` (default off), the
  `swarmcracker.balloon` label, and the `vm_balloon_mib` gauge; an invalid
  target fails the task instead of being dropped.
- **CPU pinning** — `--cpuset` on `service create`/`update` and `vm create`,
  stored canonically and applied as a cgroup v2 `cpuset.cpus` pin; an offline
  CPU fails the task naming the request and the host's online CPUs.
- **cgroup v2 enforcement** — CPU/memory/IO quotas derived from the task's
  limits, applied on both daemon start paths **without requiring the jailer**;
  controller delegation is now written (`cgroup.subtree_control`).
- **Hugepages** — `--hugepages` backs guest memory with 2M pages after a host
  pool pre-check (`HugePages_Free`, even sizing) that fails the task with the
  required/free numbers and the `vm.nr_hugepages` value to set.
- **NUMA memory placement** — `--numa-node` binds the VM's memory to host NUMA
  nodes via `cpuset.mems`, pre-checked against the online nodes and the parent
  cgroup's effective set.
- **Alerting** — `vm_crashes_total`, one-hot `vm_health`, and
  `disk_usage_bytes`/`disk_total_bytes`; default `rule_files` and `alerting`
  are wired for the first time, an Alertmanager service ships in the compose
  stack, and every shipped rule references a series that is actually written.
  New `guides/alerting.md`.

### Changed
- **Documentation** — Replaced the MkDocs Material site with an Astro Starlight
  site (`docs-site/`), deployed to Cloudflare Pages. Curated the content:
  removed obsolete planning/research/historical docs, consolidated duplicate
  pages, and corrected stale commands, config defaults, and the jailer schema.
  Old URLs (`/user/…`, `/dev/…`) redirect to the new information architecture
  via `docs-site/public/_redirects`.
- **QoS flags are tri-state on update** — `--cpuset`, `--hugepages` and
  `--numa-node` keep the existing value when omitted, clear it on an explicit
  empty/false, and are rejected together with `--rollback` (which previously
  accepted and silently dropped them).
- **`--rollback` rejects update flags** — every update flag that mutates the
  spec is refused with `--rollback` instead of being validated and dropped
  (balloon, CPU/memory limits, env, mounts, network, secrets, health,
  placement, restart/update config, mode, force).
- **Daemon cgroups no longer require the jailer** — `--enable-cgroups` (default
  true) applies the per-VM cgroup on both start paths.

### Fixed
- **VM sizing** — `--memory` and `--cpu` were accepted but ignored (VMs always
  booted with the defaults); the declared values now reach the machine config.
- **cgroup limits** — `memory.max` equalled the guest size and would OOM-kill
  the VMM (now guest + 128 MiB headroom), `io.weight` used an invalid v2 form
  and never took effect, controller delegation was never written, and
  `Stop`/`Remove` removed the cgroup **before** killing the VM (leaving a
  timed-out stop running with no quota and no pin).
- **Orphan cleanup** — a VM left behind by a daemon restart kept its cgroup
  forever.
- **Alert state** — boot failures were counted as crashes (and could drive
  `vms_running` negative), operator stops were recorded as crashes, and a clean
  self-exit leaked `vms_running`.
- **Inert alert rules** — nine shipped rules referenced series that were never
  written; removed rather than left as silent no-ops.

---

## [0.10.0] - 2026-10-05

### Added
- **Golden images** — declarative recipe format (`recipes/*.yaml`) and a builder
  (`pkg/golden`) that turns a recipe into a sealed, versioned ext4 artifact, plus
  `swarmcracker image build|list|inspect`. Runtime recipes boot their own init
  (systemd/OpenRC) and run Docker inside the microVM.
- **`pkg/image`** — exported daemon-free building blocks for reuse:
  `PullImage`, `ExtractImage`, `ExtractImageToDir`, `CreateExt4FromDir`,
  `ParseDiskSize`.
- **Golden VM creation** — `swarmcracker vm create --golden <name[@version]>`
  resolves a built artifact and boots it with its recipe init (systemd/OpenRC)
  and boot args, so container runtimes such as Docker run inside the microVM.
  The executor skips image preparation for a prebuilt rootfs.
- **Recipe matrix harness** — `test-automation/scripts/golden-matrix-test.sh`
  builds each recipe, checks the runtime inside the image, and boots it under
  Firecracker. Verified on a KVM node: AlmaLinux 9, Ubuntu 24.04, Debian 12,
  Alpine 3.20.
- **Golden services** — `swarmcracker service create --golden <name[@version]>`
  (or the `swarmcracker.golden` label) runs SwarmKit service tasks from a
  prebuilt golden rootfs; a missing artifact fails the task rather than
  silently falling back to OCI image preparation.
- **Kernel profiles** — `executor.kernel_profiles` registry resolves a golden
  image's kernel (`spec.kernel.profile`, e.g. `guest-runtime-6.1`) to a
  per-profile path, so the runtime kernel no longer has to replace the
  host-wide `kernel_path` for every VM.
- **Guest-runtime kernel script** — `scripts/build-guest-runtime-kernel.sh`
  builds the `guest-runtime` kernel profile (macvlan and netfilter `-m comment`
  support) and installs it as `/usr/share/firecracker/vmlinux-runtime`.
- **`service create --disk`** — guarantees free space in the VM rootfs via the
  `swarmcracker.disk` label (`--disk 10G`); a cached rootfs smaller than
  requested is rebuilt.
- **Unified `vm list` / `vm status`** — daemon-managed (service) VMs are
  discovered from the Firecracker socket dir and merged with CLI state;
  `vm list` gains a SERVICE column, `vm status` falls back to discovery, and
  `vm stop` refuses to kill daemon-owned VMs (SwarmKit would recreate them).
- **`vm attach`** — interactive serial console access to a running microVM via
  a per-VM Unix socket (`pkg/console`), with raw-mode stdin and Ctrl-P Ctrl-Q
  to detach.
- **Multi-node lab** — `test-automation/multinode/cluster-lab.sh` builds an N-node
  nested KVM lab, forms a SwarmKit cluster with the VXLAN overlay enabled, and
  smoke-tests cross-host microVM networking.

### Changed
- **Test coverage** — unit coverage raised to 87.6% across `./pkg/...`; CI now
  gates PRs on coverage regression vs the base branch.
- **Legacy automation removed** — the unusable Vagrant setup and legacy
  multi-node shell scripts are gone; `make test-e2e` plus `swarmcracker setup`
  and `cluster init/join` are the blessed paths (`make lab` for a local
  multi-node cluster).

### Fixed
- **Multi-host networking** — every microVM's first NIC used the same MAC,
  flapping the bridge FDB and breaking cross-host TCP; MACs are now derived
  from the task ID. Also: `cluster join` dials the manager directly (replacing
  a broken `nc`/`/dev/tcp` fallback), and deprecated command wrappers no
  longer drop the original PreRun hook.
- **dnsmasq leak** — concurrent VM starts raced the per-bridge dnsmasq restart
  and orphaned instances accumulated; the lifecycle is serialized, healthy
  servers are reused, and DNS binds to the bridge gateway only (no more
  loopback collisions).
- **Systemd units** — `RuntimeDirectory=` creates the `/run` socket dirs
  before namespace setup, so manager/worker units survive a reboot instead of
  crash-looping with status=226/NAMESPACE.
- **Detached `vm create`** — the long-lived VM no longer inherits the CLI's
  stdout, so pipelines like `vm create -d | tail` see EOF; the console is
  redirected to the VM log file.
- **Rootfs seeding safety** — seeding essential dirs no longer chmods through
  symlinks (a symlinked `/var/run` could touch the *host's* `/run`), and task
  cleanup stops probing a bogus legacy rootfs path.
- **Executor event sends** — `sendEvent` drops events when the bounded buffer
  is full instead of blocking callers for 5s (fixes a CI benchmark hang).
- **Console/lifecycle/CNI hardening** — pending stdin bytes are flushed before
  console detach; a forced kill marks the VM stopped; CNI IPAM no longer hands
  out network or broadcast addresses, VIP allocation is bounded, and
  unsupported subnets (/30, IPv6) are rejected with an explicit error.
- **Control-plane CLI, VM lifecycle, DHCP fallback, and image pulls** —
  assorted single-node deployment fixes (#4–#8, #10).
- **CI flake** — the force-kill test used a hardcoded PID that can collide
  with a real host process (kill then fails with EPERM instead of ESRCH);
  it now derives a guaranteed non-existent PID from the kernel's `pid_max`.

---

## [0.9.2] - 2026-09-27

### Removed
- **Dead code** — Whole-program reachability analysis (`deadcode` + `staticcheck U1000`) removed ~3.7k lines of unreachable production code and ~14.4k lines of tests (23 files deleted). Highlights:
  - `cmd/swarmcracker/ssh_deploy.go` — never wired to any command, referenced by nothing.
  - `pkg/security` — orphaned package with zero importers; the real jailer is `pkg/jailer`.
  - `pkg/network/discovery.go` — superseded by the Consul-based `pkg/discovery`.
  - `pkg/snapshot` API-client/process/HTTP test seam that was never wired into production.
  - Deprecated `pkg/image` init stubs and `pkg/storage` mount-based injection path.
  - Unused exported helpers across `pkg/cni`, `pkg/apiversion`, `pkg/jailer`, `pkg/executor`, `pkg/metrics`, `pkg/runtime`, `pkg/config`, `pkg/translator`.

### Changed
- **Build/lint** — Enabled the `unused` linter so dead code is caught in CI.
- **Dependencies** — `go mod tidy`: dropped `al.essio.dev/pkg/shellescape` and demoted `golang.org/x/crypto` to indirect (both were only used by the removed `ssh_deploy.go`).
- **CNI** — `CNI_ARGS` is now emitted in sorted key order for deterministic output.

### Fixed
- **Documentation** — Repaired broken links in the architecture overview after removing the stale `pkg/security` reference doc.

---

## [0.9.1] - 2026-09-22

### Fixed
- **Metrics** — Correct the `/proc/<pid>/stat` field index used for process uptime; it previously read `rsslim` instead of `starttime`.
- **Snapshots** — `cluster health` and `swarmctl` looked for snapshots in `/var/lib/swarmcracker/snapshots` while the executor writes to `/var/lib/firecracker/snapshots`; all call sites now use a single `config.DefaultSnapshotDir`.

### Changed
- **Documentation** — Full review and refresh: CLI reference rewritten from the real command tree, user/dev guides updated to current commands and versions, `mkdocs.yml` nav fixed, Vagrant paths corrected, and legacy examples/units marked deprecated.

---

## [0.9.0] - 2026-09-22

### Fixed
- **Cluster initialization** — Corrected the generated systemd units (`ReadWritePaths`, `Type=simple`, `PrivateTmp=true`, writable cache/state/CNI dirs) so the manager and worker start reliably under `ProtectSystem=strict`.
- **Cluster join** — Accept real SwarmKit join tokens (`SWMTKN-1-{hash}-{secret}`); the previous validator required a non-existent role segment and rejected every valid token.
- **MicroVM lifecycle** — Firecracker is no longer started with the caller's context, which canceled (SIGKILL) every VM as soon as `Start()` returned.
- **Container workload startup** — Alpine-family images (busybox `/sbin/init`) now run their OCI ENTRYPOINT/CMD through the injected tini wrapper instead of being left without a workload.
- **tini invocation** — Use the boolean `-s -g` flags and a numeric `-e <signal>`; the previous `-g <seconds>` / `-e QUIT` forms crashed PID 1.
- **Guest networking** — The init wrapper mounts `devtmpfs` and configures `eth0` from the kernel `ip=` parameter.
- **`setup config`** — Honor `--non-interactive` instead of prompting and failing with `kernel_path is required`.
- **`setup install --download-kernel`** — Discover kernels from the current dated Firecracker CI S3 layout.
- **Volume mounts** — `handleVolumeMount` returns an error (and no longer panics) when no volume manager is configured.
- Lint: migrate to `grpc.NewClient` and fix a `nilerr` finding (golangci-lint now reports zero issues).

### Added
- **CNI enabled by default** for `cluster init` / `cluster join` (`--enable-cni`), with graceful degradation when plugins are missing.
- **`setup install --download-cni`** to install the standard CNI plugins (bridge, host-local, loopback).

### Changed
- CI: run golangci-lint v2 via `golangci-lint-action@v9`, refresh action versions, and fix the release smoke-test VXLAN flag.
- Build: `make all` now builds `swarmd-firecracker` and `swarmcracker-agent` from their packages.
- Version references bumped to v0.9.0 (binary default, Ansible variables, docs).

---

## [0.6.0] - 2026-04-08

### Added
- **Jailer cgroup resource limits** — CPU and memory limits via cgroups for jailed VMs
- **Parent cgroup configuration** — Configurable parent cgroup for jailer VM hierarchy
- **swarmctl CLI tool** — SwarmKit cluster management (ls-nodes, ls-services, ls-tasks, create-service, rm-service)
- **SwarmKit control API integration** — mTLS authentication to SwarmKit control socket

### Fixed
- **Image extraction** — Resolve podman/docker `--quiet` flag container ID parsing
- **Jailer cgroup version** — Normalize "v2" → "2" for jailer compatibility
- **Jailer chroot resources** — Copy kernel/rootfs into jailer chroot directory
- **Jailer socket directory** — Create `/run/firecracker/` inside chroot
- **Go lint issues** — Fix naming conventions for Go Report Card A rating

---

## [0.2.1] - 2026-02-01

### Fixed
- Ansible: handle missing bridge netfilter on fresh Ubuntu
- Ansible: correct UFW firewall rule syntax
- Ansible: create extraction directory before extracting tarball
- Ansible: update Firecracker kernel URL
- Ansible: remove duplicate kernel URL key
- Ansible: make swarmctl build optional (Go version compatibility)

---

## [0.2.0] - 2026-01-31

### Added
- Multi-architecture support (amd64 + arm64)
- Rolling update support with improved status reporting
- Health checks, metrics, volumes, credential store
- One-line install script for manager and worker setup
- Ansible automation for cluster deployment
- Comprehensive installation guide

### Changed
- Improved stability — graceful shutdown, resource reporting, rootfs cleanup, VXLAN discovery

### Fixed
- Pre-existing test failures
- Lint errors in E2E tests
- Release pipeline for Linux-only builds

---

## [0.1.0] - 2026-01-30

### Added
- Initial release of SwarmCracker
- Firecracker microVM executor for SwarmKit
- Task-to-VM translation
- Network management (TAP devices, bridges, VXLAN overlay)
- VM lifecycle management (start, stop, monitor)
- Basic CLI tooling
- CI/CD pipeline (test, build, lint, release)
- One-line install script

---

[Unreleased]: https://github.com/restuhaqza/SwarmCracker/compare/v0.12.0...HEAD
[0.12.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.9.2...v0.10.0
[0.9.2]: https://github.com/restuhaqza/SwarmCracker/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/restuhaqza/SwarmCracker/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.8.0...v0.9.0
[0.6.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.5.0...v0.6.0
[0.2.1]: https://github.com/restuhaqza/SwarmCracker/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.1.0
