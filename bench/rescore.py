#!/usr/bin/env python3
"""Recounts bench rows from their run directories with the current proxy.py
summarize() and analyze.py, so rows written by older versions count alike.

Usage: rescore.py results.jsonl > results-rescored.jsonl
Adds: mem (gnosh's mem verbs), gnovm_runs, pops, slides, subagents,
summary_messages (the harness's own count), for gnosh summaries_auto and
summaries_manual (what started each) and summary_pre_tokens_max, tools (calls
by name), and for gnosh what each session printed of itself (sessions.py).
"""
import json
import os
import subprocess
import sys

here = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, here)
import analyze  # noqa: E402
import sessions  # noqa: E402

for line in open(sys.argv[1]):
    if not line.strip():
        continue
    row = json.loads(line)
    d = row.get("dir")
    if d and os.path.exists(os.path.join(d, "proxy.jsonl")):
        out = subprocess.run([sys.executable, os.path.join(here, "proxy.py"), "summarize",
                              os.path.join(d, "proxy.jsonl")], capture_output=True, text=True)
        if out.returncode == 0 and out.stdout.strip():
            row.update(json.loads(out.stdout))
        try:
            a = analyze.analyze(d)
            row["tools"] = a.get("tools")
            row["gnovm_runs"] = (a.get("tools") or {}).get("gnovm_run", 0)
            for k in ("mem", "pops", "popped", "slides", "subagents", "summary_messages", "summaries_auto",
                      "summaries_manual", "summary_pre_tokens_max", "compactions"):
                if k in a:
                    row[k] = a[k]
            if str(row.get("harness", "")).startswith("gnosh"):
                row.update(sessions.facts(d))
        except Exception as e:  # keep the row even if its store is unreadable
            row["analyze_error"] = repr(e)[:200]
    print(json.dumps(row))
