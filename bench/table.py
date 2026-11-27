#!/usr/bin/env python3
"""Markdown tables from bench rows (results.jsonl from run.sh).

Usage: table.py results.jsonl [--lane SUBSTR]...

Per lane: one table per task (harness rows) and one overall table. Numbers
are medians over reps, except "passed", "summaries", "auto summaries",
"slides" and "mem calls", which add up. Tokens in thousands. Feed it
rescore.py's output to get auto summaries, slides and mem calls. "summaries" is
the proxy's count of summary requests; "auto summaries" is how many of gnosh's
summaries it made by itself rather than for /compact (its session files).

A run that a cap ended (its wall time, or its turns) and whose check did not
pass was stopped, not failed: "passed" counts it apart, and it is listed as
STOPPED under its task.
"""
import json
import statistics
import sys
from collections import defaultdict


def med(xs):
    xs = [x for x in xs if x is not None]
    return statistics.median(xs) if xs else None


def k(x):
    return "" if x is None else ("%.0fK" % (x / 1000.0) if x >= 10000 else "%.1fK" % (x / 1000.0))


def s(x):
    return "" if x is None else ("%.0f s" % x)


def stopped(r):
    """Whether a cap ended run r before its check could pass."""
    return not r["passed"] and (r.get("outcome") == "stopped" or bool(r.get("timed_out")))


def table(rows, title):
    out = ["", "**%s**" % title, "",
           "| harness | passed | wall | requests | prompt sent | read (uncached) | generated | peak context | summaries | auto summaries | slides | mem calls | errors |",
           "|---|---|---|---|---|---|---|---|---|---|---|---|---|"]
    by = defaultdict(list)
    for r in rows:
        by[r["harness"]].append(r)
    # gnosh as it ships first, then gnosh run its other ways, then the rest.
    for h in sorted(by, key=lambda h: (not h.startswith("gnosh"), h != "gnosh", h)):
        rs = by[h]
        passed = sum(1 for r in rs if r["passed"])
        cut = sum(1 for r in rs if stopped(r))
        gn = h.startswith("gnosh")
        out.append("| %s | %d/%d%s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            h, passed, len(rs), ", %d stopped" % cut if cut else "", s(med([r["wall_s"] for r in rs])),
            med([r["requests"] for r in rs]),
            k(med([r["prompt_tokens"] for r in rs])), k(med([r["read_tokens"] for r in rs])),
            k(med([r["generated"] for r in rs])), k(med([r["peak_context"] for r in rs])),
            sum(r.get("summaries", 0) for r in rs),
            sum(r.get("summaries_auto", 0) or 0 for r in rs) if gn else "",
            sum(r.get("slides", 0) or 0 for r in rs) if gn else "",
            "%d in %d runs" % (sum(r.get("gnovm_runs", 0) or 0 for r in rs),
                               sum(1 for r in rs if r.get("gnovm_runs"))) if gn else "",
            sum(r.get("errors", 0) for r in rs)))
    return out


def main():
    path = sys.argv[1]
    lanes_filter = [a.split("=", 1)[1] for a in sys.argv[2:] if a.startswith("--lane=")]
    rows = [json.loads(l) for l in open(path) if l.strip()]
    lanes = defaultdict(list)
    for r in rows:
        if lanes_filter and not any(f in r["lane"] for f in lanes_filter):
            continue
        lanes[r["lane"]].append(r)
    out = []
    for lane, rs in lanes.items():
        out.append("### %s (window %s)" % (lane, rs[0].get("window")))
        out += table(rs, "All tasks (medians per run)")
        for task in ("rename", "algos", "bugs", "continue"):
            t = [r for r in rs if r["task"] == task]
            if t:
                fails = ["%s r%d: %s" % (r["harness"], r["rep"], r["check_msg"][:90]) for r in t if not r["passed"] and not stopped(r)]
                cuts = ["%s r%d (%s): %s" % (r["harness"], r["rep"], r.get("stopped_by") or "wall cap", r["check_msg"][:90]) for r in t if stopped(r)]
                out += table(t, task)
                if fails or cuts:
                    out.append("")
                    out += ["- FAIL " + f for f in fails]
                    out += ["- STOPPED " + f for f in cuts]
        out.append("")
    print("\n".join(out))


if __name__ == "__main__":
    main()
