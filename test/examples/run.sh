#!/bin/sh
# Run the documented SDK examples against the loopback fixture.
#
# This is the executable half of docs-site/src/content/docs/guides/sdk-examples.md:
# the mock server in ./mockserver speaks the v1 wire format on loopback, both
# examples run against it unmodified, and their output is compared byte for byte.
# It needs a Go toolchain, python3 and node; no cluster, root, Docker or network.
set -eu
unset CDPATH

ROOT=$(cd -- "$(dirname -- "$0")/../.." && pwd)
TMPROOT=${TMPDIR:-/tmp}
WORK=$(mktemp -d "${TMPROOT%/}/swarmcracker-examples.XXXXXX")
MOCK_PID=

cleanup() {
	status=$?
	# stop_mock is defined below; only call it once a fixture has been started.
	[ -z "$MOCK_PID" ] || stop_mock
	rm -rf "$WORK"
	exit "$status"
}
trap cleanup EXIT INT TERM

fail() {
	echo "examples: FAIL: $*" >&2
	exit 1
}

# -- fixture -----------------------------------------------------------------

# addr_line_ready reports whether the fixture printed a complete `addr=` line.
# A complete line ends in a newline: command substitution strips it, so an empty
# last byte means the line is whole and not a partially flushed write.
addr_line_ready() {
	[ -s "$WORK/mock.out" ] || return 1
	[ -z "$(tail -c 1 "$WORK/mock.out")" ] || return 1
	grep -q '^addr=' "$WORK/mock.out"
}

# start_mock [flags]: start the fixture and point SWARMCRACKER_HOST at it.
start_mock() {
	: >"$WORK/mock.out"
	"$WORK/mockserver" "$@" >"$WORK/mock.out" 2>"$WORK/mock.err" &
	MOCK_PID=$!

	tries=0
	while ! addr_line_ready; do
		if ! kill -0 "$MOCK_PID" 2>/dev/null; then
			cat "$WORK/mock.err" >&2
			fail "mock server exited before printing its address"
		fi
		tries=$((tries + 1))
		[ "$tries" -le 100 ] || fail "mock server did not print a complete addr= line within 10s"
		sleep 0.1
	done

	ADDR=$(sed -n 's/^addr=//p' "$WORK/mock.out" | head -n 1)
	[ -n "$ADDR" ] || fail "mock server printed no addr= line"
	echo "examples: mock server at $ADDR"

	SWARMCRACKER_HOST="http://$ADDR"
	export SWARMCRACKER_HOST
}

stop_mock() {
	if [ -n "$MOCK_PID" ] && kill -0 "$MOCK_PID" 2>/dev/null; then
		kill "$MOCK_PID" 2>/dev/null || true
		wait "$MOCK_PID" 2>/dev/null || true
	fi
	MOCK_PID=
}

echo "examples: building mock server"
(cd "$ROOT" && go build -o "$WORK/mockserver" ./test/examples/mockserver) ||
	fail "cannot build test/examples/mockserver"

unset SWARMCRACKER_CA SWARMCRACKER_TOKEN_FILE SWARMCRACKER_TASK
start_mock

# -- helpers -----------------------------------------------------------------

# compare <label> <actual> <expected>: byte-for-byte, with a unified diff on failure.
compare() {
	printf '%s\n' "$2" >"$WORK/actual"
	printf '%s\n' "$3" >"$WORK/expected"
	if ! diff -u "$WORK/expected" "$WORK/actual" >"$WORK/diff"; then
		echo "examples: FAIL: $1 did not print the documented lines" >&2
		cat "$WORK/diff" >&2
		exit 1
	fi
}

# expect_error <label> <code> <command...>: the program must print exactly one
# `error=<code>` line on stderr, nothing on stdout, and exit 1. A stray stack
# trace (an exception the program did not classify) fails the diff.
expect_error() {
	label=$1
	want=$2
	shift 2

	set +e
	"$@" >"$WORK/error.out" 2>"$WORK/error.err"
	status=$?
	set -e

	[ "$status" -eq 1 ] || fail "$label exited $status, want 1"
	[ ! -s "$WORK/error.out" ] || fail "$label printed to stdout: $(cat "$WORK/error.out")"

	printf 'error=%s\n' "$want" >"$WORK/error.expected"
	if ! diff -u "$WORK/error.expected" "$WORK/error.err" >"$WORK/error.diff"; then
		echo "examples: FAIL: $label did not print error=$want on stderr" >&2
		cat "$WORK/error.diff" >&2
		exit 1
	fi
}

run_python() {
	PYTHONPATH="$ROOT/sdk/python" python3 "$ROOT/docs-site/examples/python/example.py"
}

run_node() {
	node "$ROOT/docs-site/examples/node/example.mjs"
}

# -- read path ---------------------------------------------------------------

echo "examples: python example (read path)"
PY_OUT=$(run_python) || fail "the python example exited non-zero"
echo "examples: node example (read path)"
NODE_OUT=$(run_node) || fail "the node example exited non-zero"

READ_EXPECTED="version=0.11.0-example
capabilities=events,exec,read,write
services=2
service=web
service=api"

compare "the python read path" "$PY_OUT" "$READ_EXPECTED"
compare "the node read path" "$NODE_OUT" "$READ_EXPECTED"
compare "the two languages" "$NODE_OUT" "$PY_OUT"

# Both programs must ask for a page of two, not fetch everything and slice.
grep -q 'GET /v1/services limit=2' "$WORK/mock.err" ||
	fail "an example did not request limit=2 (fixture log: $(cat "$WORK/mock.err"))"

# -- exec path ---------------------------------------------------------------

SWARMCRACKER_TASK=t1
export SWARMCRACKER_TASK

echo "examples: python example (exec path)"
PY_TASK_OUT=$(run_python) || fail "the python example exited non-zero with SWARMCRACKER_TASK set"
echo "examples: node example (exec path)"
NODE_TASK_OUT=$(run_node) || fail "the node example exited non-zero with SWARMCRACKER_TASK set"

TASK_EXPECTED="$READ_EXPECTED
exec.exit=0
exec.stdout=example"

compare "the python exec path" "$PY_TASK_OUT" "$TASK_EXPECTED"
compare "the node exec path" "$NODE_TASK_OUT" "$TASK_EXPECTED"
compare "the two languages on the exec path" "$NODE_TASK_OUT" "$PY_TASK_OUT"

# -- non-zero guest exit -----------------------------------------------------

# A guest command that fails is a successful API call: both programs must print
# the status and stdout and still exit 0 (and so must this script, above).
echo "examples: restarting the mock server with -exit-code 3"
stop_mock
start_mock -exit-code 3

echo "examples: python example (non-zero guest exit)"
PY_NONZERO_OUT=$(run_python) || fail "the python example failed when the guest command exited 3"
echo "examples: node example (non-zero guest exit)"
NODE_NONZERO_OUT=$(run_node) || fail "the node example failed when the guest command exited 3"

NONZERO_EXPECTED="$READ_EXPECTED
exec.exit=3
exec.stdout=example"

compare "the python non-zero exit path" "$PY_NONZERO_OUT" "$NONZERO_EXPECTED"
compare "the node non-zero exit path" "$NODE_NONZERO_OUT" "$NONZERO_EXPECTED"
compare "the two languages on the non-zero exit path" "$NODE_NONZERO_OUT" "$PY_NONZERO_OUT"

# -- error contract ----------------------------------------------------------

# A failure the program can classify prints one `error=<code>` line and exits 1.
# The client constructor validates the host and reads the CA and token files, so
# a refused host or an unreadable file must be caught there too, not escape as a
# traceback.
echo "examples: checking the error contract"
unset SWARMCRACKER_TASK

SWARMCRACKER_CA="$WORK/no-such-ca.pem"
export SWARMCRACKER_CA
expect_error "the python example with an unreadable CA" usage run_python
expect_error "the node example with an unreadable CA" usage run_node
unset SWARMCRACKER_CA

SWARMCRACKER_HOST=http://192.0.2.1:4244
export SWARMCRACKER_HOST
expect_error "the python example with a non-loopback http host" usage run_python
expect_error "the node example with a non-loopback http host" usage run_node

# -- docs drift --------------------------------------------------------------

# The docs page quotes these files byte for byte apart from the import. That is a
# promise to the reader, so check it instead of trusting the last editor.
echo "examples: checking the docs snippets match the example files"
if ! python3 - "$ROOT" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
page = (root / "docs-site/src/content/docs/guides/sdk-examples.md").read_text()
python_file = (root / "docs-site/examples/python/example.py").read_text()
node_file = (root / "docs-site/examples/node/example.mjs").read_text()

def fence(language):
    blocks = re.findall(r"```" + language + r"\n(.*?)```", page, re.S)
    if len(blocks) != 1:
        sys.exit(f"expected exactly one {language} fence, found {len(blocks)}")
    return blocks[0].rstrip("\n")

# The page shows the installed-package form: no sys.path bootstrap, bare
# specifier. The bootstrap is the comment block and the sys.path.insert line.
python_expected = re.sub(
    r"(?:^# In this repository.*\n|^# snippet shows the installed form.*\n|^sys\.path\.insert\([^\n]*\n)+\n",
    "",
    python_file,
    flags=re.M,
)
# The page must not import a module it does not use: the path bootstrap is the
# only use of sys, so the import goes with it.
if "sys." not in python_expected:
    python_expected = re.sub(r"^import sys\n", "", python_expected, flags=re.M)
python_expected = python_expected.rstrip("\n")

node_expected = node_file.replace(
    "from '../../../sdk/node/src/index.mjs'", "from 'swarmcracker'"
).rstrip("\n")

for name, expected, actual in (
    ("python", python_expected, fence("python")),
    ("javascript", node_expected, fence("javascript")),
):
    if expected != actual:
        for index, (want, got) in enumerate(zip(expected.splitlines(), actual.splitlines()), 1):
            if want != got:
                sys.exit(f"{name} snippet differs at line {index}:\n  doc: {got!r}\n  file: {want!r}")
        sys.exit(f"{name} snippet differs in length")
PY
then
	fail "guides/sdk-examples.md no longer matches the example files"
fi

echo "examples: ok"
