# Acceptance tests: the MVP's evidence on a real host

`test/acceptance` is one test — `TestMVP` — that drives the two binaries a caller uses
(`sandboxd` and `swarmcracker`) through the whole MVP cycle on a real KVM host and
records what happened. It is the only path that can prove the epic's host-only criteria;
a unit test can prove the decision, not that a guest booted or that a TAP device is gone.

It runs in CI through `.github/workflows/host-evidence.yml`, which dispatches over SSH
into the lab host. See
[docs-site/src/content/docs/contributing/testing/host-evidence.md](../../docs-site/src/content/docs/contributing/testing/host-evidence.md)
for the criteria it covers and what it will not claim.

## What each criterion is called

Each line of `criteria.tsv` names the epic criterion (#121) it settles, so a red line reads
without a lookup. `3b` is the older-template half of criterion 3, and `run-clean` is the
run's own promise to the host rather than a criterion from the epic.

| `criteria.tsv` id | Epic criterion (#121) |
|---|---|
| `1-cold-create` | 1 — a cold create is ready within 4 s and reports its timings (the warm half is a pool; not measured) |
| `2-exec` | 2 — `exec` returns stdout, stderr and the exit code, separately |
| `3-cp` | 3 — `cp` moves a tree into the workspace and `exec` sees it |
| `3b-cp-old-template` | 3 — the same copy against a template whose agent predates `rooted` must be refused and write nothing |
| `4-expose` | 4 — `expose` gives an authenticated URL: 200 with its token, 401 without, dead after delete |
| `5-egress-none` | 5 — `egress: none` resolves no DNS and reaches no peer, host gateway or internet |
| `6-checkpoint-fork` | 6 — `checkpoint` + `fork --count 2` yields three isolated sandboxes |
| `8-rm` | 8 — `DELETE` frees process, jail, netns, TAP, cgroup, disk, token and record |
| `9-scoped-token` | 9 — a sandbox token cannot exec elsewhere, list, or mint tokens |
| `11-template-agent` | 11 — every template answers `hello` with protocol and capabilities; one without an agent is refused |
| `12-auth` | 12 — a non-root guest process cannot become root, and an unauthenticated host dial is rejected |
| `run-clean` | — the run added no namespace, listener or VMM to the host |

Criteria 7 (idle TTL pause/resume) and 10 (node loss, cross-node restore) have no line here:
they are outside the MVP, so this run neither measures nor claims them.

## Running it by hand

On a KVM host, as root, from a clean checkout of the commit under test:

```bash
# The two binaries the run drives. `make all` does not build sandboxd, so name it.
go build -o build/sandboxd ./cmd/sandboxd
go build -o build/swarmcracker ./cmd/swarmcracker

sudo env \
  SANDBOXD_BIN="$PWD/build/sandboxd" \
  SWARMCRACKER_BIN="$PWD/build/swarmcracker" \
  SWARMCRACKER_TEST_TEMPLATE=sandbox-base \
  SWARMCRACKER_GOLDEN_DIR=/var/lib/firecracker/golden \
  SWARMCRACKER_TEST_EVIDENCE="$PWD/.host-evidence" \
  go test -tags=acceptance -count=1 -timeout 40m -v ./test/acceptance/
```

`SANDBOXD_BIN` and `SWARMCRACKER_BIN` are not optional on a host that has other builds
installed: unset, the run resolves each name on `PATH` and exercises whatever is there
instead of the checkout. `SWARMCRACKER_TEST_EVIDENCE` is what produces `criteria.tsv`; unset,
the run still passes or fails but leaves the ledger that a run is read from unwritten.

### The template must be built from the commit under test

A template carries the guest agent embedded when it was built, so a template built before
this commit drives that commit's agent — and a `cp` to an agent without `untar` or `rooted`
is refused, which is the capability gate working rather than a bug. Criterion 3 needs a
template built from this tree.

Two things make that easy to get wrong on a host that already holds one:

- `template build` **reuses** an artifact whose recipe digest and host pin match, and the
  recipe digest does not include the guest agent binary. A plain `template build
  recipes/sandbox-base.yaml` on a host that already has `sandbox-base@1.0.0` keeps the
  stale agent. (It now refuses instead of reusing it — `template_agent_stale`, naming the
  agent the template carries and the one this tree has — because a reuse that reported
  success was the silent half of this. The new-version build below is still the way to get
  a fresh template without touching an older one.)
- `--force` does rebuild, but it overwrites the artifact at the same path, and the older
  template is exactly what criterion `3b` needs.

So build the fresh template under a new version and leave the old one in place:

```bash
sed 's/^  version: 1\.0\.0$/  version: 1.1.0/' recipes/sandbox-base.yaml \
  > /var/tmp/sandbox-base-1.1.0.yaml
# A build boots the artifact it sealed to hold its agent to the v2 handshake, so it needs
# KVM and root, and it writes into the store the run reads (/var/lib/firecracker/golden).
sudo ./build/swarmcracker template build /var/tmp/sandbox-base-1.1.0.yaml

# then run with SWARMCRACKER_TEST_TEMPLATE=sandbox-base@1.1.0, and 3b finds the
# older sandbox-base@1.0.0 by itself.
```

The run creates its own state directory, socket, jail base and rootfs under one scratch
tree and removes nothing of anybody else's. Set `SWARMCRACKER_TEST_KEEP=1` to keep that
tree for inspection; the daemon's log is `<scratch>/sandboxd.log`.

One criterion needs a template whose guest agent predates the `rooted` capability — the
stock template case that makes the capability gate load-bearing. Name it with
`SWARMCRACKER_TEST_OLDER_TEMPLATE`, or leave that unset and the run searches the template
stores (`SWARMCRACKER_GOLDEN_DIR` and `/var/lib/swarmcracker/templates`) for a manifest
whose recorded agent lacks it and which this node can boot, recording which one it chose. A
host with no such template fails that criterion: it has not measured the case.

Set `SWARMCRACKER_TEST_EVIDENCE=<dir>` to have the run append one line per criterion to
`<dir>/criteria.tsv` — that file is what the workflow uploads, and it is what a run is read
from: one line per criterion, ending in `PASS`, `FAIL`, or `NOT EVALUATED` with the reason.

The verdict is the first field of a line, and it is the **only** verdict. The lines whose id
field is empty are notes: what the criterion measured, written after its verdict and
prefixed by it (`PASS: cold create ready in 3.1 s`). They are context, not results — a
reading that takes a note for an outcome is reading the wrong column.

Every criterion runs on its own, creates what it needs, and is recorded whatever it decides:
a red criterion does not stop the ones after it, and a panic inside one is that criterion's
failure rather than the end of the run. That is not a nicety — the first run on the real host
lost seven criteria to one failed assertion, and reported it as one failure.

The same holds for a failure inside a *helper* rather than at the criterion's own assertion.
The harness's helpers assert through the criterion's own `test.T`, and the record is written
from a deferred call, so a `require` that fails deep inside one is still that criterion's
`FAIL` line. Both the second runner's symptoms — a binary that stopped at the first red
criterion, and a run whose ledger was never written — come from the opposite, and each has
a self-test that fails if it returns.

The run fails if any criterion failed **or** could not be evaluated: an unmeasured criterion
is not a criterion that held.

### What a criterion does not prove

Three limits are worth knowing before a ledger is read as more than it is:

- **Criterion 11 proves that the manifest and the guest agree, not that the template is
  current.** It reads the template's recorded agent and the `hello` the guest answers, and
  compares them; a template built from an older tree with a matching manifest answers just
  as consistently. Freshness is criterion `3b`'s subject (a template that predates a
  capability must be refused), and neither criterion is the other.
- **`run-clean` cannot prove the host is clean, in either direction.** It compares the host
  before and after this run, and its VMM check is best-effort: a jailed VMM whose argument
  line does not name the sandbox is invisible to it. What does settle the run's own
  footprint is the external `footprint-before`/`footprint-after` pair the runner script
  takes — a name in the "after" that was not in the "before" fails the run whatever the
  criteria said. `run-clean` is a second look, not the authority.
- **Criterion 8 is partly the product's own report.** The namespace, the disk, the VMM and
  the record are checked from the host; the TAP device, the cgroup and the jail are only
  checked as entries in the removal document `rm` returns. That document is the product
  describing itself, so a `rm` that under-reports a resource it did not free would still
  pass. The host-side checks are why the criterion is worth running at all.

### What the command-line guard does, and what it does not

The invariant is **one composer** (`withCLIFlags`), not "the door rejects every mistake".
`host.run` refuses exactly one shape — a CLI format flag (`-o`/`--output`) that arrived
after `--`, where only the guest's argv may be — because that is the shape that failed a
criterion three times: the flag became the guest's argument, the CLI answered in prose, and
a test that meant to read a document decoded the word "error". A fourth builder therefore
fails in a self-test rather than in a host run.

Beyond that the guard is deliberately shallow, and the reason is that a guest argv is a
legitimate place for a flag name the CLI also has. One criterion asserts that
`--host` after `--` reaches the guest, so `withCLIFlags` **moves** the flags it is given
instead of rejecting a repeated name, and a command line composed by hand out of flags that
do not collide with the CLI's will still run. What the package relies on is the convention,
checked by the tests in `harness_self_test.go`, not a total validator.

## What it needs

- Linux, root, `/dev/kvm`, `firecracker`, `jailer`, `ip`, `nft`, `lsns`, `ss`.
- A sealed template this host can boot, in the template store.
- A template whose userspace has, at minimum, `/bin/sh`, `nc` (the preview criterion's
  guest-side server is `nc` answering one request at a time — the reference template ships
  busybox without the `httpd` applet) and one of `getent`, `nslookup` or `curl` (for the
  DNS half of the egress criterion). A template that lacks one makes that criterion fail
  with the reason rather than passing by measuring nothing.

## What it does not do

It does not measure the warm path (there is no pool), it does not use a second node, and
it does not exercise pause/resume, `sandbox shell`, `attach`, `activity`/`quiesce`
consistency under write load, or a cross-node restore. Those are outside the MVP.
