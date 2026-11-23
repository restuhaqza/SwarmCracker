#!/bin/sh
# The gate's own test: the pairs it must accept and refuse.
#
# This exists because the gate's arithmetic is the kind of thing that is quietly wrong at
# exactly one value and fine everywhere else. It caught a real case: 87.2 to 87.1 was
# refused, because 87.2 - 0.1 is 87.100000000000009 in floating point and the comparison
# was made there. A test that only tried 90 -> 80 would have passed either way.
#
# Run with: sh scripts/coverage-gate.test.sh
set -u

gate="$(dirname "$0")/coverage-gate.sh"
failures=0

expect() {
	want=$1
	base=$2
	head=$3
	why=$4

	sh "$gate" "$base" "$head" >/dev/null 2>&1
	got=$?
	if [ "$got" -ne "$want" ]; then
		echo "FAIL: base $base, head $head: expected exit $want, got $got — $why"
		failures=$((failures + 1))
		return
	fi
	echo "ok: base $base, head $head -> $got ($why)"
}

# Within the allowance, and above it.
expect 0 87.2 87.1 "a drop of exactly 0.1 is allowed; 87.2 - 0.1 is 87.100000000000009 in floating point"
expect 0 87.2 87.3 "a rise is allowed"
expect 0 87.2 87.2 "holding steady is allowed"
expect 0 100.0 100.0 "a fully covered tree stays green"

# Above the allowance.
expect 1 87.2 87.0 "a drop of 0.2 is a regression"
expect 1 87.2 80.0 "a large drop is a regression"

# The boundary either side of the allowance, so the test fails if the allowance becomes
# 0.2 or 0.05 rather than 0.1.
expect 0 89.0 88.9 "exactly 0.1 below"
expect 1 89.0 88.84 "0.16 below is more than the allowance"

# The numbers are compared in rounded tenths, so a drop that rounds to 0.1 is a drop of 0.1:
# 88.85 is 889 tenths, not 888. This is the rule stated plainly rather than an accident of
# the arithmetic, since `go tool cover -func` prints one decimal and never produces such a
# value itself.
expect 0 89.0 88.85 "0.15 below rounds to 0.1, which is inside the allowance"

# The real case that exposed the floating-point comparison: a three-decimal base from
# `go tool cover -func`. It was refused by the old gate and is inside the allowance.
expect 0 86.998 86.983 "three decimals, exactly 0.015 below"

# Rounding, not truncation: 86.998 is 870 tenths, so 86.98 is within the allowance.
expect 0 86.998 86.98 "truncating instead of rounding would refuse this"

# Malformed input is its own failure, never a pass.
expect 2 "" 87.1 "an empty base is a usage error, not a pass"
expect 2 87.2 "abc" "a non-numeric head is a usage error, not a pass"

if [ "$failures" -ne 0 ]; then
	echo "$failures coverage-gate case(s) failed"
	exit 1
fi
echo "coverage gate: all cases pass"
