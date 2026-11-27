#!/bin/bash
# selftest.sh: for every task, check.sh must fail on the task as built and
# pass on the solved copy. Uses $GO like the checks do.
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
bad=0
for t in rename algos bugs continue recall; do
	bash "$here/$t/make.sh" "$tmp/$t" >/dev/null || { echo "$t: make.sh failed"; bad=1; continue; }
	bash "$here/$t/make.sh" --solved "$tmp/$t-solved" >/dev/null || { echo "$t: make.sh --solved failed"; bad=1; continue; }
	if msg=$(bash "$here/$t/check.sh" "$tmp/$t"); then
		echo "$t: BAD, the unsolved task passes: $msg"; bad=1
	else
		echo "$t: unsolved -> $msg"
	fi
	if msg=$(bash "$here/$t/check.sh" "$tmp/$t-solved"); then
		echo "$t: solved   -> $msg"
	else
		echo "$t: BAD, the solved task fails: $msg"; bad=1
	fi
done
exit $bad
