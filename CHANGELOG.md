# Changelog

All notable changes to SwarmCracker will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
