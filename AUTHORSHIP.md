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

16 November 2026, on the home AI box (passed on by the session running
scenarios there):

> let gnosh my prompt be mmm... white, sure, and the reponse (not thinking) be sorta green, neo

Set: what he types is white and the model's answers are green, its thinking
as it was (`30d07c7`). Claude's part: the green is #4ADE80 on a terminal of
true colour and the bright green of the sixteen otherwise, and on a terminal
that says its background is light both take darker colours that read there.

16 November 2026, after a Claude Code session of his had gone past a "then
prompt me" in his order once its conversation was summarized:

> make a report on why you missed my command, the bug report on gnosh.ai, and also how gnosh is better; make a scenario and test that gnosh does better, or fix it until it is to match the website. i mean, use the ai box etc, but after what you're currently working on

Set: the scenario `stop-where-told` (a long order with "then prompt me"
before its last step), run on the AI box with mem and without, and the
summary's wording, which now keeps every stop the person set
(`cb3234a`). Claude's part: the same runs showed a summary calling 32 fixes
done of which 6 were made, so a summary now keeps what was done apart from
what was only found (`13d2c49`); and a todo whose start kept sliding out
while the model read its files again, so a slide now names the files its
rounds edited (`70a55ce`).

Asked of the earlier gnosh on 19 October 2026 (`spec/gnosh/harness-cli.md`,
CLI-12), and built in this one on 18 November 2026:

> how do i inspect or edit the context window for each prompt using crush?

Set: `gnosh prompt show`, `path` and `export` (`spec/DECISIONS.md` item 109).
Claude's part: what fills the window, tool by tool, stays `/context all`
rather than the spec's `gnosh prompt tools` and `context`, and a text printed
alone goes to standard output, with what it is on standard error, so that it
can be kept in a file.

18 November 2026, to another of Claude's sessions, days after he had
noticed that Claude Code's working line turns yellow and says "deep in
thought" when its model thinks for long:

> do something like the yellow -> deep in thought thing except keep it multi colored sparkly as crush/gnosh was, except this means start with more boring bland even colors first. in terms of direction of movement i think we do movement like dropping rightward, as if accelerating to the right, like gravity goes sideways, and when "deep in thought", more things drop faster with higher acceleration.

Set: the working mark says how long the model has been in one thought; it
starts in bland, even colours and becomes many-coloured and sparkling; its
marks drop to the right as if gravity ran sideways; in a deep thought more
of them drop, faster, under a stronger pull; and the words "deep in
thought" (`spec/DECISIONS.md` item 116). Claude's part: the figures (a
thought deepens from five seconds on and is deep at forty; one drop at a
time under a pull of 5 cells a second each second while calm, three at a
time under a pull of 30 when deep; colour comes before the rest), the tail
that stretches with a drop's speed, the glints, the grey the colours start
near, and that the fall is drawn and timed from his words and not from how
the other program's line or the earlier program's animation is made.

The same day, of the strip of marks that came of it:

> make it two part (it already is) where the first part is like claude, i think a single letter, and then the second part is the text that starts non-white and the whole text is animated. the thing that moves rightward is a different color of the text, but the text is always the same... and the transition to fasater/more rightward should be faster

Set: one changing mark before the words; the words themselves in a colour
that is not white; what moves to the right is colour passing over the
letters, which stay as they are; and the deep state comes sooner
(`spec/DECISIONS.md` item 138). Claude's part: reading "the transition
should be faster" as the build-up (three to fifteen seconds of a thought,
where it was five to forty), the grey the words stand in, the tail that
fades into the letters, the limit on a drop's speed, and that the mark's
shapes are gnosh's own.

18 November 2026, of the licence he had asked for on 13 November ("GNO
Network GPL latest with strong attribution"):

> make sure the new gnosh license is good, strong attribution too, given what i said i wanted when i asked you for it, aand that there are no conflicts with other licensses, and is independent of crush

Set: the licence was checked against his words and found as he asked
(version 6 is still the latest; its text is gno.land's word for word; the
Strong Attribution clause stands with his Designation), with nothing of the
other program's licence in it; the notes in both licence files now say why
none of the libraries gnosh is built with conflicts with it, and no longer
tell the other program's story. The interface now shows the credits both
licences ask for, and `/license` the legal notices (`spec/DECISIONS.md`
item 139). Claude's part: where the credits stand (the last line of the
session's header), the words of `/license`, and making it the licence's
"Appropriate Legal Notices" so that a work made from gnosh must show them
too.

19 November 2026, of the empty lines he had asked on 14 November to take
out ("in general don't print empty lines"):

> put the blank lines back into gnosh...

Set: the interface puts its empty lines back where they stood before 14
November: a blank line between the blocks of the scrollback (one-line tool
calls, notices and questions still stand under each other), a blank row
above and below what is still being written, and one before the change a
question shows.

21 November 2026, using gnosh over ssh on the Mac Studio, with a screenshot
of the working line, the prompt, five tasks under it and the status line:

> gnosh as i'm using it ssh studio... the txt field (and attached lines see clipboard do not stay on the bottom of the screen... and the todos should be above the prompt not below it... things like that. vs claude code

Set: the task list stands above the prompt box, under the line that says
what gnosh is doing, and the box with its status line is the last thing on
the screen, as in Claude Code. Once the live region has come down to the
bottom of the screen it stays there, and lines that move from it to the
scrollback are drawn once (`spec/DECISIONS.md` item 387). Claude's part:
finding the two causes, keeping the box down with blank rows above the
region rather than below it, following the cursor's row through the
renderer's own writes, and leaving the region where it is until it first
reaches the bottom, as Claude Code's classic screen does.

The same evening, with a screenshot of a mem.Ask call whose receipt
quoted the question again under the code that held it:

> see clipboard... there's redundancy

Set: the receipt under a Gno call no longer quotes what the call's code
already says; what mem decided stays (`spec/DECISIONS.md` item 388).
Claude's part: doing it on the screen only, matching the code's strings
as mem cuts and flattens them, and leaving the model's wording to him.

Later that evening, of an answer quoting jarvis behind the purple bar:

> see clipboard... the stuff to the right of the purple bar shouldn't be green.

Set: a quote's words are in the terminal's own colour, behind the purple bar
(`spec/DECISIONS.md` item 389). Claude's part: the terminal's own colour
rather than a grey, so that a long quote stays easy to read.

22 November 2026, after the box fix shipped and he logged in to the Mac
Studio again, inside tmux:

> it's still doing that thing here the text field is not fixed on bottom of screen.

Set: the box stands on the bottom rows from the first frame, whatever is
above it (`spec/DECISIONS.md` item 390).

26 November 2026, over ssh to the Mac Studio, of an answer he copied from the
screen with a blank row between "...flatter a bad" and "lemma, though, ...":

> ssh studio green text has newlines where it shouldn't

and "yes" to taking the blank row out.

Set: a paragraph's open row stands right under its rows already in the
scrollback, with no blank row between them, while the model finishes it or
writes a call after it (`spec/DECISIONS.md` item 391). Claude's part: the
blank rows that keep the box on the bottom rows go below the open row too, so
that a paragraph on a screen not yet full stays in one piece as well.

The same day, over ssh on the Mac Studio, after a long conversation there
had kept him waiting minutes for each answer:

> but /clear doesn't work when it's thinking on a previous prompt... i have to wait. i don't want to

Set: /clear typed while a turn runs stops the turn and opens the new
session as soon as the turn has stopped (`spec/DECISIONS.md` item 392).
Claude's part: stopping the turn first rather than clearing under it,
putting what he had queued back in the box, and letting a side question
that waits go, as /exit does.

Later that day, of the line saying "running cat > /tmp/bucket/main.go
<<'EOF' · 2m 05s · Esc stops":

> why is it stuck seemingly

It was not: the `cat` had ended at once, and the time went to the `go run
main.go | tail -15` after it, which the line did not name. Asked whether
the line should name what runs:

> do it

Set: while a command runs, the line names the programs its shell runs just
now, as in "running go run main.go, tail -15" (`spec/DECISIONS.md` item
394). Claude's part: finding them among the processes of the command's
session by the shell's own arguments, naming them with commas rather than
guessing at pipes, and waiting two seconds, then naming a program once it
has been seen twice.

Later still:

> allow gnosh to restart itself, like when i say 'restart yourself', or when it thinks it needs to, but keep a root PID so gnosh can still be killed externally

Set: a Restart tool the model calls, which starts gnosh again in the
session by exec, in place of itself, so that the process id stays
(`spec/DECISIONS.md` item 396). Claude's part: going on with the task by
itself afterwards, as a moved session does; refusing a second restart
until he has said something; never asking first, as EnterWorktree does
not; and the test at a real terminal that the id stays and that a SIGTERM
to it still ends gnosh.

Meanwhile, of the colours of the working line in a thought:

> when gnosh is thinking i see the multi colored spinner (whereas awhen running bash and waiting it is white), but the color scheme isn't what i want when multi colored... remember the old gnosh color scheme? I think it was green, yellow but mostly purple and something...

Set: a deep thought's drops are mostly purple, with the mint, a pink and
the gold between, the earlier gnosh's colours (`spec/DECISIONS.md` item
397). Claude's part: finding the earlier palette in its notes (Crush's
purple and hot pink, with green and gold hints), four purples in seven,
and a second purple that stays purple in 256 colours.

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
