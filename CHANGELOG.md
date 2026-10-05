# Changelog

All notable changes to SwarmCracker will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- **Multi-node lab** — `test-automation/scripts/cluster-lab.sh` builds an N-node
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
- End-to-end test report: `docs/reports/e2e-two-vm-2026-09-21.md`.

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

[Unreleased]: https://github.com/restuhaqza/SwarmCracker/compare/v0.10.0...HEAD
[0.10.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.9.2...v0.10.0
[0.9.2]: https://github.com/restuhaqza/SwarmCracker/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/restuhaqza/SwarmCracker/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.8.0...v0.9.0
[0.6.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.5.0...v0.6.0
[0.2.1]: https://github.com/restuhaqza/SwarmCracker/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.1.0
