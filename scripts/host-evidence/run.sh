#!/usr/bin/env bash
#
# The MVP's evidence run on one lab host, dispatched from CI.
#
# What this is for: seven of the epic's acceptance criteria need KVM, a network
# namespace and the jailer, so nothing in the orb or in the normal CI runner can prove
# them. This script is the one path that does, and it is deliberately manual
# (workflow_dispatch, or a PR labelled `host-evidence`): the host is single, stateful and
# shared, so a run must be a decision rather than a consequence of opening a pull
# request.
#
# The shape is: build on the host, run the acceptance package as root, and then check
# that the host is exactly as clean as it was before. A run that leaves a namespace, a
# listener or a VMM behind is a failure, not a warning — the host carries leftovers from
# earlier work, and the only way to keep that from mattering is for every run to prove it
# added none.
#
# Environment (set by .github/workflows/host-evidence.yml):
#   SANDBOXD_BIN, SWARMCRACKER_BIN   the binaries to exercise (built from this checkout)
#   SWARMCRACKER_TEST_TEMPLATE       the template to boot (default sandbox-base)
#   SWARMCRACKER_GOLDEN_DIR          the template store (default /var/lib/firecracker/golden)
#   ACCEPTANCE_EVIDENCE_DIR          where criteria.tsv and the logs are written
#
# Usage: run.sh [--skip-build]. The workflow builds the binaries as the login user, so
# the module cache it already has is the one used, and then runs this script under sudo
# with --skip-build: a build as root would want its own cache and the network.
#
# The script never prints a secret, never calls `gh` (the lab host's `gh` is not
# authorised), and fails closed when a prerequisite is missing.

set -euo pipefail

SKIP_BUILD=0
for arg in "$@"; do
	case "${arg}" in
	--skip-build) SKIP_BUILD=1 ;;
	*) echo "unknown argument: ${arg}" >&2; exit 2 ;;
	esac
done

EVIDENCE_DIR="${ACCEPTANCE_EVIDENCE_DIR:-$(pwd)/.host-evidence}"
SUMMARY="${EVIDENCE_DIR}/summary.txt"
RUN_LOG="${EVIDENCE_DIR}/acceptance.log"

mkdir -p "${EVIDENCE_DIR}"

say() { printf '%s\n' "$*" | tee -a "${SUMMARY}"; }

fail() {
	say "FAIL: $*"
	exit 1
}

# The footprint this script can ask the host for without Go: namespaces named after a
# sandbox, listeners on a preview's derived loopback address, and Firecracker processes.
# The Go package counts the same three, and the two are compared so a run cannot report
# clean by looking in the wrong place.
footprint() {
	local out="$1"
	{
		printf '## namespaces\n'
		ip netns list 2>/dev/null | awk '{print $1}' | grep '^sbx-' || true
		printf '## preview listeners\n'
		ss -ltnH 2>/dev/null | awk '{print $4}' | grep '^127\.' | grep -v '^127\.0\.0\.1:' || true
		printf '## vms\n'
		# By the executable's name, not the argument line: `pgrep -af firecracker` matches
		# the shell that runs it, so this script would list its own footprint as a leftover
		# VMM. The Go package counts the same three, and the two are compared so a run
		# cannot report clean by looking in the wrong place.
		ps -eo pid=,args= 2>/dev/null | awk '{ n=split($2, p, "/"); if (p[n] == "firecracker") print }' | grep '/sbx-' || true
	} >"${out}"
}

say "==> the host"
say "    uname:    $(uname -srm)"
say "    kvm:      $(test -e /dev/kvm && echo present || echo MISSING)"
say "    firecracker: $(command -v firecracker || echo MISSING)"
say "    jailer:   $(command -v jailer || echo MISSING)"

test -e /dev/kvm || fail "/dev/kvm is not available: this run needs a KVM host"
test -n "$(command -v firecracker)" || fail "firecracker is not on PATH"

# The host's state before the run. It is expected to be non-empty: the lab host carries
# leftovers from earlier work, and this run's job is to add none of its own, not to
# clean somebody else's.
BEFORE="${EVIDENCE_DIR}/footprint-before.txt"
footprint "${BEFORE}"
say "==> the host before the run: $(grep -c '^' "${BEFORE}" || true) lines of footprint (see footprint-before.txt)"

if [ "${SKIP_BUILD}" -eq 0 ]; then
	say "==> building the binaries from this checkout"
	mkdir -p "$(dirname "${SANDBOXD_BIN}")" "$(dirname "${SWARMCRACKER_BIN}")"
	GOFLAGS="${GOFLAGS:-}" go build -o "${SANDBOXD_BIN}" ./cmd/sandboxd
	GOFLAGS="${GOFLAGS:-}" go build -o "${SWARMCRACKER_BIN}" ./cmd/swarmcracker
else
	say "==> using the binaries already built"
	test -x "${SANDBOXD_BIN}" || fail "${SANDBOXD_BIN} is not there"
	test -x "${SWARMCRACKER_BIN}" || fail "${SWARMCRACKER_BIN} is not there"
fi
say "    sandboxd:     ${SANDBOXD_BIN}"
say "    swarmcracker: ${SWARMCRACKER_BIN}"

say "==> the acceptance run"
set +e
SWARMCRACKER_TEST_EVIDENCE="${EVIDENCE_DIR}" \
	SWARMCRACKER_TEST_TEMPLATE="${SWARMCRACKER_TEST_TEMPLATE:-sandbox-base}" \
	SWARMCRACKER_GOLDEN_DIR="${SWARMCRACKER_GOLDEN_DIR:-/var/lib/firecracker/golden}" \
	SANDBOXD_BIN="${SANDBOXD_BIN}" \
	SWARMCRACKER_BIN="${SWARMCRACKER_BIN}" \
	go test -tags=acceptance -count=1 -timeout 40m -v ./test/acceptance/ 2>&1 | tee "${RUN_LOG}"
TEST_STATUS=${PIPESTATUS[0]}
set -e

# The footprint after, compared with the footprint before: what this run added. A
# non-empty difference is the run's own failure whatever the tests said, because a
# sandbox nobody removed is a sandbox the next run inherits.
AFTER="${EVIDENCE_DIR}/footprint-after.txt"
footprint "${AFTER}"

ADDED=$(comm -13 <(sort "${BEFORE}") <(sort "${AFTER}") || true)
if [ -n "${ADDED}" ]; then
	say "==> this run left something behind:"
	printf '%s\n' "${ADDED}" | tee -a "${SUMMARY}"
	say "FAIL: the host is not as clean as the run found it"
	exit 1
fi
say "==> no namespace, preview listener or VMM of this run remains"

# The ledger is what the run is read from: one line per criterion, PASS, FAIL or
# NOT EVALUATED. The test binary's exit status cannot say which criterion was red and
# which never ran, and it cannot say whether a red one hid the rest.
LEDGER="${EVIDENCE_DIR}/criteria.tsv"
if [ ! -s "${LEDGER}" ]; then
	say "FAIL: the run recorded no criteria, so nothing it did can be claimed (see acceptance.log)"
	exit 1
fi

# Count the ledger by its first field, which is what a verdict is. A criterion's notes
# follow its verdict, prefixed by it ("PASS: …"), so counting by suffix would count them
# as criteria.
PASSED=$(awk -F'\t' '$1 != "" && $3 == "PASS"' "${LEDGER}" | wc -l)
FAILED=$(awk -F'\t' '$1 != "" && $3 == "FAIL"' "${LEDGER}" | wc -l)
MISSING=$(awk -F'\t' '$1 != "" && $3 ~ /^NOT EVALUATED/' "${LEDGER}" | wc -l)

say "==> criteria: ${PASSED} passed, ${FAILED} failed, ${MISSING} not evaluated"
awk -F'\t' '$1 != "" && $3 == "FAIL"' "${LEDGER}" | while IFS= read -r line; do say "    FAILED: ${line}"; done
awk -F'\t' '$1 != "" && $3 ~ /^NOT EVALUATED/' "${LEDGER}" | while IFS= read -r line; do say "    NOT EVALUATED: ${line}"; done

if [ "${FAILED}" -ne 0 ] || [ "${MISSING}" -ne 0 ]; then
	say "FAIL: ${FAILED} criterion/criteria failed and ${MISSING} could not be evaluated (see criteria.tsv)"
	exit 1
fi

if [ "${TEST_STATUS}" -ne 0 ]; then
	say "FAIL: the acceptance run exited ${TEST_STATUS} although its ledger has no red criterion (see acceptance.log)"
	exit "${TEST_STATUS}"
fi

say "PASS: ${PASSED} criteria all held on this host"
