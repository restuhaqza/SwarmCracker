# Acceptance tests: the MVP's evidence on a real host

`test/acceptance` is one test — `TestMVP` — that drives the two binaries a caller uses
(`sandboxd` and `swarmcracker`) through the whole MVP cycle on a real KVM host and
records what happened. It is the only path that can prove the epic's host-only criteria;
a unit test can prove the decision, not that a guest booted or that a TAP device is gone.

It runs in CI through `.github/workflows/host-evidence.yml`, which dispatches over SSH
into the lab host. See
[docs-site/src/content/docs/contributing/testing/host-evidence.md](../../docs-site/src/content/docs/contributing/testing/host-evidence.md)
for the criteria it covers and what it will not claim.

## Running it by hand

On a KVM host, as root, from the repository root:

```bash
make all
sudo env \
  SANDBOXD_BIN="$PWD/build/sandboxd" \
  SWARMCRACKER_BIN="$PWD/build/swarmcracker" \
  SWARMCRACKER_TEST_TEMPLATE=sandbox-base \
  SWARMCRACKER_GOLDEN_DIR=/var/lib/firecracker/golden \
  go test -tags=acceptance -count=1 -timeout 40m -v ./test/acceptance/
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

Every criterion runs on its own, creates what it needs, and is recorded whatever it decides:
a red criterion does not stop the ones after it, and a panic inside one is that criterion's
failure rather than the end of the run. That is not a nicety — the first run on the real host
lost seven criteria to one failed assertion, and reported it as one failure.

The run fails if any criterion failed **or** could not be evaluated: an unmeasured criterion
is not a criterion that held.

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
