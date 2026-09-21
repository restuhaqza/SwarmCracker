# (Deprecated) Systemd Service Files

> ⚠️ **Deprecated.** These manual unit files run the upstream `swarmd` binary and
> predate SwarmCracker's generated units. Do **not** use them for new deployments.

SwarmCracker now generates and manages its own systemd units:

- `swarmcracker-manager.service` — created by `swarmcracker cluster init`
- `swarmcracker-worker.service` — created by `swarmcracker cluster join`

Both run `/usr/local/bin/swarmd-firecracker` (the SwarmKit agent with the Firecracker
executor), with the appropriate flags, security hardening, and `ReadWritePaths`.

See the [Getting Started guide](../../docs/user/getting-started/README.md) for the
supported setup flow.

---

## Historical Reference (do not use)

The `swarmd-manager.service` / `swarmd-worker.service` files in this directory are
retained only as historical examples of an older manual setup. They invoke
`/usr/local/bin/swarmd` directly and expect hand-managed join tokens.

If you are debugging an old install:

- Logs: `journalctl -u swarmd-manager -f` / `journalctl -u swarmd-worker -f`
- Migrate by running `swarmcracker cluster init` (managers) or
  `swarmcracker cluster join <manager-addr> --token <token>` (workers).
