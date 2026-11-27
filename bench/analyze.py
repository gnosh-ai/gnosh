#!/usr/bin/env python3
"""What the model did in a bench run: tool calls by name, and for gnosh the
mem statements it ran (pushes, pops, todos, facts), the todos it popped and
the slides.

Usage: analyze.py <run dir>...   -> one JSON line per run

Reads the harness's own session store in the run directory:
  gnosh  home/.local/share/gnosh/projects/*/*.jsonl   (sessions.py)
  crush  data/crush.db
  codex  home/.codex/sessions/**/rollout-*.jsonl
"""
import collections
import glob
import json
import os
import re
import sqlite3
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sessions  # noqa: E402

MEM_RE = re.compile(r"\bmem\.([A-Z][A-Za-z]*)\s*\(")


def from_db(path):
    tools = collections.Counter()
    mem = collections.Counter()
    out = {"popped": 0, "summary_messages": 0, "sessions": 0}
    # mode=ro reads a live database's WAL too; immutable=1 is the fallback
    # for a copy without its -shm file.
    c = sqlite3.connect("file:%s?mode=ro" % path, uri=True)
    try:
        cols = [r[1] for r in c.execute("pragma table_info(messages)")]
    except sqlite3.OperationalError:
        c = sqlite3.connect("file:%s?mode=ro&immutable=1" % path, uri=True)
        cols = [r[1] for r in c.execute("pragma table_info(messages)")]
    sel = "parts, is_summary_message" + (", popped" if "popped" in cols else ", 0")
    for parts, is_summary, popped in c.execute("select %s from messages" % sel):
        out["summary_messages"] += 1 if is_summary else 0
        out["popped"] += 1 if popped else 0
        try:
            parts = json.loads(parts)
        except (TypeError, ValueError):
            continue
        for p in parts or []:
            if p.get("type") != "tool_call":
                continue
            d = p.get("data") or {}
            name = d.get("name") or "?"
            tools[name] += 1
            if name == "gnovm_run":
                for verb in MEM_RE.findall(d.get("input") or ""):
                    mem[verb] += 1
    out["sessions"] = c.execute("select count(*) from sessions").fetchone()[0]
    out["tools"] = dict(tools.most_common())
    if mem:
        out["mem"] = dict(mem.most_common())
    return out


def from_sessions(root):
    """What gnosh's session files under root say: every session of the run,
    its sub-agents' included (a swarm's sessions share one store)."""
    tools = collections.Counter()
    mem = collections.Counter()
    main, subs = sessions.session_files(root), sessions.subagent_files(root)
    out = {"pops": 0, "slides": 0, "summary_messages": 0, "summaries_auto": 0, "summaries_manual": 0,
           "summary_pre_tokens_max": 0, "sessions": len(main), "subagents": len(subs)}
    for path in main + subs:
        ents = sessions.entries(path)
        for e in ents:
            if e.get("type") != "summary":
                continue
            out["summary_messages"] += 1
            # The loop's fields on a summary entry say what started it: "auto"
            # (gnosh making room by itself, a retry after a too-long refusal
            # included) or "manual" (/compact), and about how long the
            # conversation was before it. The same values go out in
            # stream-json's compact_boundary. An entry from before gnosh kept
            # them counts in summary_messages only.
            loop = e.get("loop") if isinstance(e.get("loop"), dict) else {}
            if loop.get("trigger") in ("auto", "manual"):
                out["summaries_" + loop["trigger"]] += 1
            pre = loop.get("preTokens")
            if isinstance(pre, int) and pre > out["summary_pre_tokens_max"]:
                out["summary_pre_tokens_max"] = pre
        out["pops"] += len(sessions.frames(ents, "pop"))
        out["slides"] += len(sessions.frames(ents, "slide"))
        for _, name, inp in sessions.calls(ents):
            tools[name] += 1
            if name == "gnovm_run":
                for verb in MEM_RE.findall(inp.get("code") or ""):
                    mem[verb] += 1
    out["tools"] = dict(tools.most_common())
    if mem:
        out["mem"] = dict(mem.most_common())
    return out


def from_codex(home):
    tools = collections.Counter()
    out = {"compactions": 0, "sessions": 0}
    for f in glob.glob(os.path.join(home, ".codex", "sessions", "**", "*.jsonl"), recursive=True):
        out["sessions"] += 1
        for line in open(f, errors="replace"):
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            p = ev.get("payload") or {}
            t = ev.get("type")
            if t == "response_item" and p.get("type") in ("function_call", "custom_tool_call", "local_shell_call"):
                tools[p.get("name") or p.get("type")] += 1
            if t == "compacted" or p.get("type") in ("compacted", "context_compacted"):
                out["compactions"] += 1
    out["tools"] = dict(tools.most_common())
    return out


def merge(total, part):
    """Adds part's counts into total (a run may hold more than one store)."""
    for k, v in part.items():
        if isinstance(v, dict):
            t = total.setdefault(k, {})
            for kk, vv in v.items():
                t[kk] = t.get(kk, 0) + vv
        elif isinstance(v, (int, float)):
            total[k] = total.get(k, 0) + v
    return total


def analyze(d):
    """What the model did in run d."""
    res = {"dir": d}
    if sessions.session_files(sessions.store(d)):
        res.update(from_sessions(sessions.store(d)))
        return res
    dbs = sorted(glob.glob(os.path.join(d, "data*", "crush.db")))
    if dbs:
        total = {}
        for db in dbs:
            merge(total, from_db(db))
        res.update(total)
        return res
    if os.path.isdir(os.path.join(d, "home", ".codex")):
        res.update(from_codex(os.path.join(d, "home")))
    return res


if __name__ == "__main__":
    for d in sys.argv[1:]:
        try:
            print(json.dumps(analyze(d.rstrip("/"))))
        except Exception as e:  # one bad run must not stop the rest
            print(json.dumps({"dir": d, "error": repr(e)[:200]}))
