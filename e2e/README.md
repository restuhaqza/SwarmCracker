# E2E Testing

This directory previously held an ad-hoc log of a QEMU-based worker experiment.
That content was outdated and has been removed.

Current end-to-end testing lives in:

- **[`test/e2e/README.md`](../test/e2e/README.md)** — how to run the automated E2E suite.
- **[`docs-site/src/content/docs/contributing/testing/e2e-tests.md`](../docs-site/src/content/docs/contributing/testing/e2e-tests.md)** — E2E test architecture.
- **[`docs/reports/e2e-two-vm-2026-09-21.md`](../docs/reports/e2e-two-vm-2026-09-21.md)** — latest two-node (manager + worker) E2E report.

For a reproducible multi-node cluster, use the `swarmcracker setup` + `cluster
init/join` flow (see [Getting Started](../docs-site/src/content/docs/getting-started/index.md)),
or the single-host lab in
[`test-automation/multinode/`](../test-automation/multinode/README.md).
