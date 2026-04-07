# Changelog

All notable changes to SwarmCracker will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `CODE_OF_CONDUCT.md` — Contributor Covenant v2.1
- `SECURITY.md` — Vulnerability reporting and security model
- `.github/ISSUE_TEMPLATE/bug_report.md` — Bug report template
- `.github/ISSUE_TEMPLATE/feature_request.md` — Feature request template
- `.github/PULL_REQUEST_TEMPLATE.md` — PR template
- `CHANGELOG.md` — This file

### Changed
- Rewrote `CONTRIBUTING.md` with project-specific development guide

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

[Unreleased]: https://github.com/restuhaqza/SwarmCracker/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/restuhaqza/SwarmCracker/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/restuhaqza/SwarmCracker/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.1.0
