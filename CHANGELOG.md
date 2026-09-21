# Changelog

All notable changes to SwarmCracker will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/restuhaqza/SwarmCracker/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.5.0...v0.6.0
[0.2.1]: https://github.com/restuhaqza/SwarmCracker/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.1.0
