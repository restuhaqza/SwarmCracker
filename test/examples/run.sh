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
	if [ -n "$MOCK_PID" ] && kill -0 "$MOCK_PID" 2>/dev/null; then
		kill "$MOCK_PID" 2>/dev/null || true
		wait "$MOCK_PID" 2>/dev/null || true
	fi
	rm -rf "$WORK"
	exit "$status"
}
trap cleanup EXIT INT TERM

fail() {
	echo "examples: FAIL: $*" >&2
	exit 1
}

# -- fixture -----------------------------------------------------------------

echo "examples: building mock server"
(cd "$ROOT" && go build -o "$WORK/mockserver" ./test/examples/mockserver) ||
	fail "cannot build test/examples/mockserver"

"$WORK/mockserver" >"$WORK/mock.out" 2>"$WORK/mock.err" &
MOCK_PID=$!

tries=0
while [ ! -s "$WORK/mock.out" ]; do
	if ! kill -0 "$MOCK_PID" 2>/dev/null; then
		cat "$WORK/mock.err" >&2
		fail "mock server exited before printing its address"
	fi
	tries=$((tries + 1))
	[ "$tries" -le 100 ] || fail "mock server did not print addr= within 10s"
	sleep 0.1
done

ADDR=$(sed -n 's/^addr=//p' "$WORK/mock.out" | head -n 1)
[ -n "$ADDR" ] || fail "mock server printed no addr= line"
echo "examples: mock server at $ADDR"

SWARMCRACKER_HOST="http://$ADDR"
export SWARMCRACKER_HOST
unset SWARMCRACKER_CA SWARMCRACKER_TOKEN_FILE SWARMCRACKER_TASK

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

# The page shows the installed-package form: no sys.path line, bare specifier.
python_expected = re.sub(r"sys\.path\.insert\([^\n]*\n\n", "", python_file).rstrip("\n")
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
