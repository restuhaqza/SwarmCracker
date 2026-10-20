# Plan — #95: service `--memory`/`--cpu` are silently ignored

Branch: `fix/service-resource-sizing` · Issue: [#95](https://github.com/restuhaqza/SwarmCracker/issues/95)
Found while verifying #47 part 1 (memory balloon) — see `docs/plans/issue-47-memory-balloon.md`.
Small slice: it removes a silently-ignored flag, which is the project's standing contract.

## Current state (measured)

- **CLI** writes the request to `Resources.Limits` only:
  `cmd/swarmcracker/cmd_service_spec.go` (create) and the `service update` path.
- **Agent** copies `Reservations` only into the internal task:
  `pkg/swarmkit/executor.go:1493-1499` (`types.ResourceRequirements{Reservations: …}`);
  `pkg/types/task.go:151-154` `Resources{NanoCPUs, MemoryBytes}` carries no Limit/Reservation
  distinction.
- **Daemon translator** (`pkg/swarmkit/translator.go`, the one the daemon builds at
  `pkg/swarmkit/executor.go:663`) sizes `vcpus`/`memoryMB` from `Reservations` only, with
  defaults 1 / 512 and a silent floor: `if memoryMB < 512 { memoryMB = 512 }`.
- The **other** translator (`pkg/translator`) reads `Limits` — so the two producers
  disagree about where the size comes from.
- **Measured on the lab**: `service create --memory 1024m` booted a VM with
  `GET /machine-config` → `{"vcpu_count":1,"mem_size_mib":512}`; the guest agreed
  (`MemTotal: 493052 kB`).

## Design

A VM is a **hard allocation**, so it must be sized from the hard cap (`Limits`) when the
user declared one, falling back to `Reservations` (the soft reservation), and only then to
the defaults:

```
size = Limits (if set)  >  Reservations (if set)  >  default 1 vCPU / 512 MiB
```

An **explicit** size is honoured as asked — including a value below 512 MiB, which today is
silently rounded up to 512. 512 remains the *default* when nothing is declared. Both
translators must agree on this source so that CLI-side validation can never pass where the
node then refuses.

### Interfaces (binding)

```go
// pkg/types/task.go — ResourceRequirements gains a Limits arm mirroring Reservations.
type ResourceRequirements struct {
	Limits       *Resources
	Reservations *Resources
}

// pkg/swarmkit/translator.go — one helper, tested in isolation:
// resolveSize returns the vCPU count and memory (MiB) a VM should be booted with:
// Limits when present, else Reservations, else the 1 vCPU / 512 MiB defaults. A
// declared value is returned as declared (no silent floor).
func resolveSize(res *types.ResourceRequirements) (vcpus int, memoryMB int)
```

## Global constraints

- Go 1.26. `gofmt -l pkg cmd` must print nothing.
- No flag/field may be silently ignored or silently rounded: honour what was asked, and
  if something cannot be honoured, fail with an error naming the value.
- A change must not alter behaviour when nothing is declared (defaults stay 1 / 512).
- Tests: table-driven + testify, host- and privilege-independent. **A command-level test
  that loads configuration must supply it explicitly** (`--config` with a temp file), never
  rely on the ambient `/etc/swarmcracker/config.yaml` — this bit slice 3 in CI.
- Tests run on the Linux test server (`root@test-server`, checkout `/root/sc-verify`);
  this workstation is darwin and `cmd/...` does not build here.
- Pre-existing root-only failures exist in `pkg/image`, `pkg/swarmkit`, `pkg/network`;
  verify with `/root/sc-cmp.sh <base-sha> <pkgs...>` and require an **empty head-only** set.
- Docs live in `docs-site/src/content/docs/`.

## Task T1 — size the VM from the declared value

Goal: `--memory`/`--cpu` really size the VM, and both translators read the same field.

Files: `pkg/types/task.go`, `pkg/swarmkit/executor.go`, `pkg/swarmkit/translator.go`,
`pkg/translator/translator.go` (only if it needs to move to the shared helper), and their
`_test.go` files.

Steps:
1. Add `Limits *Resources` to `types.ResourceRequirements` (mirroring `Reservations`).
2. In the agent's conversion (`pkg/swarmkit/executor.go:1493-1499`), copy **both**
   `Limits` and `Reservations` from the API task instead of only `Reservations`.
3. In `pkg/swarmkit/translator.go`, replace the `Reservations`-only sizing with
   `resolveSize` as specified above. Keep the defaults at 1 / 512 when nothing is declared,
   and **drop the silent `< 512` floor**: an explicit `--memory 256m` must produce a 256 MiB
   VM. Non-integer/negative/absent values keep today's behaviour (defaults).
4. Make `pkg/translator`'s sizing use the same precedence (`Limits` → `Reservations` →
   defaults) so the two producers cannot disagree; if it already does, say so with
   `file:line` in your report and leave it alone.
5. Note in the report whether anything else in the repo sizes a VM from resources (grep
   for `Reservations`), so nothing is left half-converted.

Verification: `go test -count=1 ./pkg/translator/... ./pkg/swarmkit/... ./pkg/types/...`
plus `bash /root/sc-cmp.sh 8701758 ./pkg/translator/ ./pkg/swarmkit/ ./pkg/types/` with an
empty head-only set. Tests must cover: `Limits` alone → used; `Reservations` alone → used;
both set → `Limits` wins; neither → 1 / 512; an explicit 256 MiB → 256 (the floor is gone);
vCPUs from the same source as memory; and a fixture identical in both translator packages
pinning the same answer.

Depends on: —

## Task T2 — documentation

Goal: the docs say what actually happens.

Files: `docs-site/src/content/docs/reference/cli.md`, and
`docs-site/src/content/docs/guides/advanced.md` if it discusses resource sizing.

Steps:
1. Document `--cpu`/`--memory` on `service create` (and `--cpu-limit`/`--memory-limit` on
   `service update`) as sizing the VM: hard cap semantics, that the VM is sized from the
   declared value, defaults 1 vCPU / 512 MiB when nothing is declared, and that a declared
   value is honoured as asked (including below 512 MiB).
2. If the balloon docs (added in #47 part 1) contain a claim that depended on this bug —
   e.g. about "the size the node will actually boot" — verify it is now true and correct it
   if not.
3. Verify with the docs pipeline (`cd docs-site && npm run verify` on Node 22).

Verification: docs pipeline passes; `grep` shows the sizing semantics documented.

Depends on: T1.

## Verification (program-level, by the controller)

- `go test` for the touched packages on the test server.
- `/root/sc-cmp.sh` with an empty head-only set, plus `verify.sh`.
- CI green (test / lint / build / coverage gate). **CI is the only non-root, config-less
  environment**: any new command-level test must pass there.
- **Lab e2e (2 nodes)**: `service create --memory 1024m --cpu 2` → the task's
  `GET /machine-config` reports `mem_size_mib: 1024` and `vcpu_count: 2` (previously 512/1),
  and the guest's `MemTotal` agrees; then the combination that failed before
  (`--memory 1024m --balloon-mib 768`) must now be accepted by the CLI **and** boot, with
  `GET /balloon` reporting 768. Control: a service with no size flags still boots 512/1.
  Restore the lab to `main` afterwards.
- Independent review before merge.

## Acceptance mapping (#95)

| Criterion | Where |
|---|---|
| `--memory`/`--cpu` size the VM | T1 + lab e2e (`GET /machine-config`) |
| CLI validation and node sizing agree | T1 (one precedence, both translators) + the e2e combo |
| No silent rounding of an explicit value | T1 (floor removed) + test |
| Nothing changes when nothing is declared | T1 (defaults test) + e2e control |
| Docs match | T2 |

## Out of scope (recorded)

- Separate `--reserve-*` flags (soft reservations as a distinct user-facing concept) — this
  slice only makes the *existing* flags work; `Reservations` stays supported as a fallback
  source for callers that set it.
- The config loader's unwrapped error (a host without `/etc/swarmcracker/config.yaml` fails
  hard instead of falling back to defaults) — recorded separately in the program ledger.
