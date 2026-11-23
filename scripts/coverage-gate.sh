#!/bin/sh
# coverage-gate.sh — decide whether a PR's coverage may stand, compared with its base.
#
# The rule is "a PR may be at most 0.1 percentage points below the branch it targets".
# Comparing that in floating point is wrong, and wrong in a way that only shows up at the
# boundary: 87.2 - 0.1 is 87.100000000000009, so an honest drop from 87.2 to 87.1 fails
# against a limit it exactly meets. Someone then either lowers the threshold or adds tests
# they did not need; the gate should not put them in that position.
#
# So both numbers are converted to tenths of a percent as integers and compared there.
# Rounding to the nearest tenth first — `int(x * 10 + 0.5)` for non-negative x — makes
# "0.1 below" mean exactly one tenth, whatever the decimal's binary representation is.
#
# Usage: coverage-gate.sh <base-percent> <head-percent>
#   0  the head is within 0.1 of the base (or above it)
#   1  the head regressed by more than 0.1
#   2  an argument is not a percentage
#
# Both arguments must be percentages as `go tool cover -func` prints them, without the
# sign: 87.2, 86.998, 100.0.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $(basename "$0") <base-percent> <head-percent>" >&2
	exit 2
fi

base=$1
head=$2

# A percentage with an optional fraction. Anything else — an empty string from a failed
# `go tool cover`, a number with a unit attached — is a usage error rather than a value to
# guess at: silently treating it as zero would pass every gate.
for value in "$base" "$head"; do
	case $value in
	'' | *[!0-9.]* | . | *.*.*)
		echo "coverage-gate: not a percentage: '$value'" >&2
		exit 2
		;;
	esac
done

awk -v b="$base" -v h="$head" 'BEGIN {
	# Integer tenths, rounded rather than truncated: 86.998 is 870 tenths, not 869.
	bt = int(b * 10 + 0.5)
	ht = int(h * 10 + 0.5)
	if (ht < bt - 1) exit 1
	exit 0
}' || exit 1

exit 0
