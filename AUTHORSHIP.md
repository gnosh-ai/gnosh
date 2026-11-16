# Authorship

gnosh is Jae Kwon's. The idea (an agent whose context is a call stack, with
its plan kept in Gno), the name, the direction and the rulings are his. The
code and the documents in this repository were typed by Claude (Anthropic's
model, working as a set of agents) at his direction, from the prompts below.
This file keeps his words so that it stays clear what came from him and what
Claude chose on its own.

## Jae's prompts, in order

Night of 12 to 13 November 2026 (Pacific time):

> ok so how about we just implement a Go harness from scratch and remove crush entirely? one liner

Set: gnosh is rewritten from an empty repository, with no Crush code.

> i don't know about weeks. use subagents and do it now. remember that the main thing besides gnovm integration (and mem.* etc) is that i tried to make crush more claude code like. so, what's the plan? where do we start? I want it in Go.

Set: the build runs as parallel agents, starting at once; the language is Go;
the three things that matter are the GnoVM integration, `mem`, and being like
Claude Code.

13 November 2026, on the three things Claude had left for him:

> "own syntax". i don't know anything there, yaml? license: GNO Network GPL latest with strong attribution; -y yolo lol, sure, not sure what you mean by eyes on the screen.

Set: gnosh's config is its own (Claude chose JSON with comments and said
why); the licence; `-y` works as root.

Evening of 13 November 2026, when Claude put two more choices to him:

> use best judgement. use best judgement for the other thing too.

> try not to prompt me for anything, keep going and pushing, use best judgemenet.

Set: Claude decides what it would otherwise have asked, and says what it
chose. What it chose in this repository on that footing: the three rulings
on hosts and the three on the config build (`spec/DECISIONS.md` items 20 to
25), and starting the first measured bench pass on the home AI box that
night (`bench/README.md` had left when to measure to him).

14 November 2026, after he had tried the new interface:

> i dont like how the thinking just disappears after two lines... what about ctrl-o or whatever to expand at least? or whatever claude does. and i don't like the red '>'. make it one of the blue/green from gnosh.ai site. and i don't like that there's no spinner now... bring back the previous spinner with colors. and what about artifacts side tab? also pressing 'exit' doesn't exit anymore. and does '/' bring up all the commands?

Set: the prompt mark is the site's blue-green; there is a spinner with
colours again; the word `exit` leaves; how to see the thinking is said every
time; deliverables are to be shown. Claude's part: the spinner is a comet of
sparkle marks of gnosh's own and not the earlier program's animation, and in
an interface that has no side panel the deliverables become lines and a
command (`spec/gnosh/harness-ui.md`, UI-33).

The same evening, about a line the new gnosh printed at every start on the
home AI box:

> warning: mem due: "rotate the staging API key" (rotate-staging-api-key) (since 2026-11-06) what is this about

It was a todo left by a demonstration, and it was forgotten with `/mem
forget`. Claude's part, from what that showed: after an erasure gnosh no
longer tells the person to "run the same /mem redact again", which cannot be
done after a forget; the lines gnosh says for itself about mem, which are
kept in a session's file, lose an erased text like its other copies; and a
session is named to the person without the text that was erased.

Later that evening, in the new interface on the home AI box:

> remove the newline after prompt and before done etc, in general don't print empty lines

Set: the interface prints no empty line of its own, between the blocks of
the scrollback or around what is still being written and the prompt box.
Claude's part: the empty lines between the paragraphs of the model's own
answer stay, because they are the answer's.

15 November 2026, setting the work that went on while he was away:

> set a 1m loop and keep improving UX and featureset, make it claude code like. and everything else i said.

Set: gnosh is made more like Claude Code a pass at a time, each pass built
and tested on a server, pushed, and installed on the home AI boxes once the
clean-copy CI passes. Claude's part: which thing comes next, each time.

Later that day:

> i want a real side panel as an option i guess.

Set: `/panel` and the `sidePanel` option (`spec/DECISIONS.md` item 44).
Claude's part: the panel is a second way of drawing the session, on the
whole screen, and it never comes on by itself.

> make it public.

Set: this repository is public.

16 November 2026, on the home AI box (passed on by the session running
scenarios there):

> the modal doesn't seem to pop up when i type ssh ai and /model

Set: `/model` with nothing after it opens a picker, as Claude Code's and the
earlier gnosh's did (`spec/DECISIONS.md` item 50). Claude's part: the
thinking level is asked next, the cursor on the level the switch would take
anyway; and, by the same rule, `/config` and `/export` with nothing after them
open pickers too (item 51), and Ctrl+O became Claude Code's transcript view
(item 49), which his 14 November words had asked for.

16 November 2026, with a screenshot of the task list under the prompt, the
line in hand three times over:

> see clipboard, why is there repetition

Set: a line of the task list reads the frame the session stands in only
where it is that todo, and the others read their own words. Claude's part:
the cause was a session holding three todos of its plan at once, each pushed
from inside the one before; mem's receipts now say when a push stands on an
open todo and where a pop comes back to, and the session's settings files are
taken in while it runs (item 55), found in the same session's log.

16 November 2026, about gnosh.ai's speed claims, which were the earlier
gnosh's:

> do it all but 4. and then prompt me, and then work on making the speed claims true.

He then chose to rent an H200 for the measurement, as no H100 was to be had
that night.

Set: the claims measured again on this gnosh (`bench/README.md`, "Several
sessions on one plan"). Claude's part: three sessions on one plan were
slower than one, because nothing told a session that others worked it;
`mem.Show()` and `mem.Next()` now say how many other sessions work the plan,
and a person's session joins the plan's peers when its turn begins
(`8aca3c4`, `2f6e839`). With that, three sessions were 1.8× faster than
one; six sessions with a planner lost to one on llama.cpp and were 1.6×
faster than one on vLLM.

## Standing rules of his that shaped the build

- "in terms of naming conventions and such i want it to be more like claude
  code whereever reasonable, because i'm familiar with claude code the most."
  (22 October 2026)
- "keep in mind that any software/library dependencies must be assessed in
  their security by measure of how stable/finalized/annealed they are; e.g.
  active projects can't be good candidates because they may be compromised in
  commits." (1 November 2026)
- "no, if it's not done, it's not done. after 10 not dones, say, the harness
  should have ai think and see if there is a good reason to stop, before
  stopping, but otherwise it should generally keep going." (22 October 2026)
- "stop doing work on this laptop you're killing cpu ... use digital ocean
  figure it out" (20 October 2026): builds and tests run on a server.
- "just remove this kind of stuff. nobody has downloaded except me. this is
  bloatware." (23 October 2026): no code for users who do not exist.
- Three times he asked why he could not select and copy text in the earlier
  gnosh (20 and 21 October, 9 November 2026).
- The earlier gnosh's design rulings on `mem`, frames, peers and the swarm,
  recorded with their dates in `spec/gnosh/`.

## What Claude decided on its own

Each of these is Claude's choice, made because it follows from the rules
above. Jae can overrule any of them.

- No Charm libraries at all, and no third-party libraries beyond the Go
  team's `x/term` and `x/sys`: the terminal layer, the provider clients, the
  Markdown renderer and the search tools are gnosh's own code.
- An inline interface in the terminal's own scrollback instead of a
  full-screen one, so that selection and copy are the terminal's.
- The split into a spec side and a build side (PROVENANCE.md), and the cut
  of the packages (ARCHITECTURE.md).
- Sessions as append-only JSONL files rather than a database.
- The shapes in `internal/core`.
- A non-interactive run does not approve its own tool calls unless told to
  (`spec/DECISIONS.md` item 11), where the earlier gnosh did.
- An independent audit of the frame rule against its spec, and an attack on
  the permission engine with a real shell as the judge, before either was
  relied on.
- The rest of `spec/DECISIONS.md`: each item there says which of his rules
  it follows from.
