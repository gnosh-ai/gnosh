#!/bin/bash
# check.sh DIR: passes when all six service packages and the standalone summary
# package pass their tests, summary does not import sibling packages, and all
# test files and log fixtures match the original byte for byte.
here=$(cd "$(dirname "$0")" && pwd)
dir=${1:?usage: check.sh DIR}
GO=${GO:-go}
export GOTOOLCHAIN=local GOFLAGS=-mod=mod
fail() { echo "FAIL: $*"; exit 1; }
gotest() { timeout -k 5 150 "$GO" test -timeout 60s "$@"; }
cd "$dir" || fail "no directory $dir"

ref=$(mktemp -d)
trap 'rm -rf "$ref"' EXIT
bash "$here/make.sh" "$ref/r" >/dev/null 2>&1 || fail "could not build the reference"

changed=""
for f in $(cd "$ref/r" && find . \( -name '*_test.go' -o -path './logs/*.log' \) | sort); do
	cmp -s "$ref/r/$f" "$f" || changed="$changed ${f#./}"
done

if grep -qE '"example\.com/ledgercheck/(alpha|beta|gamma|delta|epsilon|zeta)"' summary/report.go 2>/dev/null; then
	fail "summary/report.go must remain standalone and not import sibling service packages"
fi

passed=0 open=""
for pkg in alpha beta gamma delta epsilon zeta summary; do
	if gotest -count=1 "./$pkg" >/dev/null 2>&1; then
		passed=$((passed + 1))
	else
		open="$open $pkg"
	fi
done

[ -z "$changed" ] || fail "$passed/7 packages passing, but protected files changed:$changed"
[ "$passed" = 7 ] || fail "$passed/7 packages passing; still failing:$open"
out=$(gotest -count=1 ./... 2>&1) || fail "7/7 packages pass individually, but ./... fails: $(echo "$out" | grep -m3 -E 'FAIL|panic' | tr '\n' ' ')"
echo "PASS: 7/7 packages passing (6 services + cross-context summary), tests and logs untouched"
