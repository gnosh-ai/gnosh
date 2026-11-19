# Decisions

Rulings on questions the specs left open. Each follows from a rule Jae has
already given; none is new policy. Jae can overrule any of them. Builders
follow this file where `spec/cc/` or `spec/gnosh/` leaves a choice.

**Which spec wins.** `spec/gnosh/` records what Jae asked his harness to do
and beats `spec/cc/` where the two differ. `spec/cc/` records Claude Code's
public conventions and is followed wherever `spec/gnosh/` is silent. Where
both are silent, the builder chooses, and says so in its report.

## From the Claude Code conventions spec (`spec/cc/INDEX.md`)

1. **gnosh reads its own settings files, not Claude Code's.** The files have
   Claude Code's JSON shape (`permissions`, `hooks`, `env`, ...), so one can
   be copied or linked across, but gnosh does not look in `~/.claude` or
   `.claude` by itself. Another program's hooks would otherwise run inside
   gnosh: Jae's own Claude Code prompt hook adds about 10 KB to every prompt,
   which suits Claude and not a local model with a 64K window. Approvals a
   person saves go to gnosh's own project-local file. (Exact file names and
   how they sit beside `gnoshrc`: `spec/gnosh/harness-config.md`.)
2. **An untrusted folder can only restrict, in every mode.** Until a folder
   is trusted, nothing of its own that runs or grants applies, in print
   mode as much as interactively: its `gnoshrc`, hooks, `env`, allow rules,
   providers, hosts, servers and options all wait. What only takes
   something away still applies (its deny rules, its lists of tools and
   skills to turn off), and so does what is only text for the model and
   runs nothing: its instruction files, with imports confined to the
   project, the skills and commands inside it, and its sessions
   (`spec/gnosh/harness-trust-and-safety.md` SAFE-2 and SAFE-3, which are
   Jae's design of 1 November 2026 and win here). `--trust-folder` trusts
   it for one run. This is stricter than Claude Code, which runs an
   untrusted folder's hooks and applies its `env`.
3. **`Glob` and `Grep` stay in the default tool set.** They are gnosh's own
   code with no outside program behind them, and a local model does better
   with a search tool than with a shell pipeline.
4. **No cap on Stop-hook continuations.** A count never ends the work. After
   every tenth forced continuation in a row (`harness-hooks.md` HOOK-21 and
   `harness-agent-loop.md` LOOP-20, which win over Claude Code's figure of
   8; an option), the model is asked to judge whether there is a real
   reason to stop; if it stops, the person is shown that it did and why.
   Jae, 22 October 2026: "no, if it's not done, it's not done. after 10 not
   dones, say, the harness should have ai think and see if there is a good
   reason to stop, before stopping, but otherwise it should generally keep
   going." `stop_hook_active` is still sent to hooks.
5. **The `init` line says `gnosh_version`**, and lists the sub-agent tool as
   `Agent`.
6. **Subprocesses get `GNOSH=1`** and not `CLAUDECODE=1`. gnosh does not
   pass itself off as another program.
7. **No commit attribution by default.** gnosh adds no trailer lines to
   commits or pull requests unless a setting asks for them.

## From the orchestrating session

8. **Directories.** Config in `$XDG_CONFIG_HOME/gnosh` (default
   `~/.config/gnosh`), data (sessions, mem) in `$XDG_DATA_HOME/gnosh`
   (default `~/.local/share/gnosh`), cache in `$XDG_CACHE_HOME/gnosh`
   (default `~/.cache/gnosh`). A project's own files are in `.gnosh/` at its
   root. Tests point all three variables and `HOME` at temp dirs.
9. **Messages are gnosh's own words.** Error lines, notices, prompts and tool
   descriptions are written fresh, in plain English. What gnosh takes from
   Claude Code is names: flags, tools, fields, values, file names.

## From the harness spec (`spec/gnosh/harness-open-questions.md`)

10. **Every recommended reading in that file is adopted**, with the one
    change in item 11. The ones that shape the build most:
    - `gnoshrc` is a list of settings, not a script; a shell construct in it
      is a startup error that names the replacement; `--base-url` may be
      given more than once and the first that answers wins (OQ-1). Jae's
      three old-named files are read in place until he moves them.
    - `-c` and `-C` both mean continue; `--cwd` has no short form (OQ-9).
    - For a local model server there is no limit before the first piece of
      a reply and 900 seconds between pieces; for a remote one, ten minutes
      to start and 60 seconds between pieces; `request-timeout` overrides
      both (OQ-13).
    - Thinking is on by default and `/no_think` sticks (OQ-18).
    - No general list of banned commands; with hosts declared, a command
      that starts with `ssh` or `scp` is refused with a pointer to hosts
      (OQ-5).
    - `/loop` keeps Jae's meaning, "keep working until done" (OQ-17).
    - No counter ends work on empty replies: gnosh keeps asking and tells
      the person at every third one (OQ-7).
    - The JSON settings file has Claude Code's shape only (`permissions`,
      `hooks`, `env`); everything gnosh-specific is in `gnoshrc`; an unknown
      key is an error (OQ-8).
    - Claude Code's tool names everywhere. gnosh's deliverables tool is
      called `Deliver`, never `Artifact` (OQ-10).
    - Hook variables are `GNOSH_*` plus Claude Code's two; `AGENT=gnosh`
      (OQ-21). Of the inherited hook-protocol extras only exit 3 is kept
      (OQ-24).
    - No tool reaches a site without asking; no analytics; no client/server
      mode; no import of old state or old sessions (OQ-6, 22, 23, 25).
    - The colours are gnosh's own purple and hot pink, not the earlier
      program's; Jae picks from rendered options later (OQ-26).
11. **A non-interactive run does not approve its own tool calls** (OQ-2;
    this overrides `harness-cli.md` CLI-31). `gnosh -p` and `gnosh run`
    refuse what would have asked, as Claude Code does, unless the run is
    given `-y` / `--dangerously-skip-permissions`, a permission mode, or
    allow rules. gnosh starts closed (PRIN-1) and follows Claude Code
    (PRIN-3); the two agree here. Jae's unattended runs on the AI box keep
    working without touching each script by setting the mode once in that
    machine's own user settings, at switch-over and with his say.

## From the core spec (`spec/gnosh/open-questions.md`)

12. **Every reading taken in that file is adopted: build it as it landed.**
    Holds are soft for plans and hard for leaves (Q1); a pop spares the
    prompts of the turn it is made in (Q2); the slide is built as specified
    and tested against a server whose window really is 32K (Q3); the root
    lists todos in the order made and `mem.Next()` works there (Q13, Q14);
    stock sub-agents have no gnovm tools (Q15); the goal judge reads the
    full conversation (Q16); the mem reminder is not muted by a goal (Q18);
    a reviewed close is not judged twice (Q19); reminders go in as
    user-side rows (Q20); the fresh guard stays (Q21). The swarm bridge and
    frames on gno.land are not built in this rewrite (Q5, Q6). The questions
    marked "For Jae" there stay his.
13. **How the earlier harness's rows map onto the new transcript.** Where
    the core spec says "row" it means a transcript entry; "flag the rows"
    means appending an entry that names the entries it marks, since nothing
    in a transcript is rewritten; "hidden parts of a prompt" are user-side
    entries of their own, stored in the order sent and joined into one
    message when the request is built, so what is rebuilt from the
    transcript is byte-identical to what went out.

## From the similarity audit (`tools/audit/similarity/`)

14. **`AskUserQuestion` takes Claude Code's documented arguments**
    (`spec/cc/tools.md`), not the argument names `harness-tools.md` TOOL-1
    gives it, which were the earlier program's question tool's and so
    Crush's design. gnosh follows Claude Code's conventions (PRIN-3), and
    nothing of Jae's depends on the other names.
15. **Agent definitions and skills are read from gnosh's own folders first**
    (`<config>/gnosh/agents`, `.gnosh/agents`), and from Claude Code's
    (`~/.claude/agents`, a trusted project's `.claude/agents`) after them,
    so that what Jae already has keeps working. They are text for a model
    and run nothing; a project's are read only when the folder is trusted.
16. **The config file's language** (`provider add …`, `option …`) is the
    earlier program's, and so Crush's design; the code that reads it is
    gnosh's own. It is kept so that Jae's existing files load, as he ruled
    for his own files (PRIN-5). Whether gnosh gets a config syntax of its
    own before it is published is his to decide.

## From Jae, 13 November 2026

17. **gnosh gets a config syntax of its own.** Jae: "own syntax". Asked which,
    he left it open ("i don't know anything there, yaml?"). It is one JSON
    file with comments allowed, the same file that already holds
    `permissions`, `hooks` and `env` in Claude Code's shape, with gnosh's own
    keys beside them (`providers`, `models`, `hosts`, `options`), in
    camelCase as Claude Code's settings are. Not YAML: Go has no YAML reader
    in its standard library, so it would mean either a dependency (against
    decision 1 of ARCHITECTURE.md) or a hand-made reader of a format with
    many surprises; JSON is in the standard library and is what Jae already
    edits for Claude Code. The `gnoshrc` line language, the reading of the
    earlier program's files, and every name taken from it go (the second
    similarity audit found names that had not gone; items 26 to 28 say what
    became of each). This replaces items 10 (first point) and 16 above. Jae's three existing files are
    converted for him by hand at switch-over; gnosh ships no converter.
18. **The licence** is the GNO Network General Public License, latest
    version, with the Strong Attribution terms. Jae: "GNO Network GPL latest
    with strong attribution". The latest on that day is version 6 of 25
    November 2025, checked against gno's own repository. `LICENSE.md` holds
    it with the Additional Terms as Jae set them on 29 October 2026.
19. **`-y` is not refused for root.** Jae: "-y yolo lol, sure". Claude Code
    refuses to skip permissions as root; gnosh does not, because Jae runs as
    root on his machines.

## From the hosts build, 13 November 2026

20. **A machine whose key ssh does not already know is refused, not
    accepted.** TOOL-20 records that the earlier gnosh took a machine's key
    the first time it saw it. The new one runs ssh with strict host-key
    checking. The first connection to a machine is the one moment anything
    on the network can answer in its place, and an agent that accepts the
    key by itself means nobody ever looked at the fingerprint. The person
    connects once by hand (`ssh user@address`), and from then on gnosh's
    commands go through. Machines already in the account's `known_hosts`
    are not affected. Claude's choice, from "security-first"; one word in
    `sshArgs` (`accept-new`) gives the earlier behaviour back.
21. **A command on a host starts in the account's home directory, every
    time.** Bash has no working-directory argument (Claude Code's has
    none), so TOOL-20's "with a working directory" has nothing to attach
    to, and the session's directory is a path on this machine that is never
    sent. A command that needs another directory says `cd dir && …`.
22. **A command on a host never moves to the background.** Stopping the
    local ssh does not stop a quiet program on the other machine, so gnosh
    refuses `run_in_background` together with `host`, stops such a command
    at its time limit, and says in the result what may still be running
    there.

## From the config build, 14 November 2026

23. **A variable that is not set leaves its provider out, with a warning.**
    `${NAME}` with no default in a provider's address, key or header value,
    when NAME is not set, does not stop startup: that provider is left out,
    and a warning names the variable, the key and the line (`gnosh doctor`
    shows it). A config that lists a hosted provider therefore still starts
    on a machine where that provider's key is not exported. The earlier
    line format did the same for `$NAME`. One word in `finish.go` makes it
    a stop.
24. **What the new syntax does not carry over**, because nothing in gnosh
    acts on it: `notifications`, named hooks and the removing of a hook, the
    directives for MCP and language servers, model prices, six provider
    types nothing speaks to (openrouter, vercel, google, google-vertex,
    azure, bedrock), and the options and variables for analytics, an update
    check and a provider catalogue. A key for any of them is an unknown key
    now, which stops startup and says so. `openai-compat` is spelled
    `openai-compatible`.
25. **A leftover `gnoshrc` is neither read nor warned about**: no code for
    users who do not exist. Jae's own files are converted by hand at
    switch-over.

## From the second similarity audit, 14 November 2026

The audit (`tools/audit/similarity/README.md`) found no code of Charm's and
no code shaped like it. It found names: parts of gnosh's interface that the
specs had written down from the earlier gnosh, and that the earlier gnosh
had from Crush. Jae had ruled that such names go (item 17) and left the
choices to Claude ("use best judgement"). Each name is either changed (26)
or kept with the reason said (27).

26. **Names changed.**
    - `discoverModels` is `listModels`: the provider APIs call the request
      "list models".
    - `disabledSkills` is `hiddenSkills`: what it does.
    - The state file's two slots, `large` and `small`, are `model` and
      `smallModel`, as the config's keys are.
    - `GNOSH_PROFILE` is `GNOSH_DEBUG_PAGE`: what it serves.
    - `GNOSH_EVENT`, the one hook variable that did not follow Claude
      Code's field names, is `GNOSH_HOOK_EVENT_NAME` (the field is
      `hook_event_name`).
    - The terminal layer's `Options.EscTimeout` is `Options.EscWait`, with
      its three fellows of the first audit.
    - The command line loses what Claude Code's own flags already do:
      `gnosh run` and its `-q` (print mode is `-p`), `-s`/`--session`
      (`--resume` and `--session-id`), and `-D`/`--data-dir`. Commands
      listed for parts that are not built are taken off the list.
    - The provider types `ollama`, `lmstudio`, `litellm` and `omlx` go:
      together with `llamacpp` they were the earlier program's set, nobody
      uses them here, and `openai-compatible` speaks to all four servers.
27. **Names kept, though Crush has them too, and why.**
    - `options`, as the object that holds gnosh's own switches: a plain
      word, and the layout Jae was shown in item 17.
    - `debug` (Claude Code's `--debug`) and `requestTimeout` (the provider
      SDKs' own term).
    - `-y` and `--yolo`: Jae's own word for it (item 19).
    - `--cwd` (item 10) and `--small-model`, which is the key `smallModel`
      as a flag.
    - The commands `session`, `models`, `logs`, `dirs`, `projects` and
      `stats`, where they are built: plain nouns for what they show, which
      other harnesses use as well.
    - The variables a hook is given: each is a field of Claude Code's
      documented hook input in upper case behind `GNOSH_`
      (`session_id`, `cwd`, `tool_name`, `tool_input.command` and so on),
      for a script that would rather not read JSON. `GNOSH_PROJECT_DIR`
      stands beside `CLAUDE_PROJECT_DIR`.
    - `llamacpp`, `openai-compatible`, `anthropic`, `openai`: the servers'
      and the formats' own names.
    - `gnosh.json`, `.gnosh/`, `GNOSH.md`: the program's own name in the
      pattern Claude Code uses too (`.claude/`, `CLAUDE.md`).
    - The front end's task type (`Content`, `ActiveForm`, `Status`): the
      fields of Claude Code's `TodoWrite`.
28. **The edit-distance routine of `internal/cli/suggest.go` stays.** It has
    the shape of one in the earlier gnosh's own code (not Charm's). It is
    the textbook routine, written again: the transcript of the builder that
    wrote it shows no read of the earlier tree or of the staging folder,
    and the earlier file's name does not occur in it. It was not carried,
    so `CARRIED.md` does not list it.
29. **`--allow-dangerously-skip-permissions` is on the command line.** Item
    26 took it off with every name whose part was not built. Its part was
    built all but the flag: the config already knew a session in which
    skipping is within reach without being the mode it starts in, the
    session refused the switch anywhere else, and Shift+Tab already passed
    over the mode where it was refused. The flag is Claude Code's, by its
    name and its meaning (`spec/cc/cli.md`). Where a setting forbids
    skipping, the flag is refused as `-y` is.
30. **MCP servers are configured in files of their own, in Claude Code's
    shape.** The user's are in `mcp.json` in gnosh's config folder, and a
    project's in `.mcp.json` at its root, which counts only in a trusted
    folder (each entry is a program to run, like a project's hooks). The
    user's file also holds, under `"projects"` by each project's root, the
    servers of one project that are the person's alone: Claude Code's
    local scope, in the shape of its `~/.claude.json`, so that the three
    scopes `gnosh mcp add -s` names (local, the default; project; user)
    are two files. A name in more than one is the local entry, else the
    project's, whole. Not in gnosh.json: spec/cc/mcp.md
    recommends this split, Claude Code keeps MCP servers out of its
    settings files too, and so a `.mcp.json` written for Claude Code works
    in gnosh unchanged while gnosh.json keeps its strict keys. A run (`-p`)
    waits for the servers before its turn; an interactive session connects
    them in the background. Servers gnosh starts (stdio) and servers at a
    URL (Streamable HTTP; the old SSE transport is refused by name) are
    both spoken to; a server at a URL is sent no roots, since the folders
    open here are nothing it needs to know, and is not followed to another
    host by a redirect, which would carry its headers there. `gnosh mcp
    add`, `add-json`, `get`, `list` and `remove` are Claude Code's; they
    change one entry and leave the rest of the file, and the order of its
    keys, as they were. `list` and `get` connect to each server to say
    whether it works, but never start a project's servers in a folder that
    is not trusted, and never show the values of variables and headers.
    `--mcp-config` gives a run servers of its own, files or the JSON
    itself, whose entries win; `--strict-mcp-config` makes them the only
    ones; and safe mode, which turns off what files set up, leaves them
    on, since they were typed for this run. `MCP_TIMEOUT` and
    `MCP_TOOL_TIMEOUT` are read as Claude Code reads them.
31. **A sub-agent runs in the background only in a session someone
    attends, and only when asked.** The Agent call takes
    `run_in_background` (and a definition `background: true`), as
    Claude Code's does; the sub-agent then works beside the session, and
    its report reaches the model as a notice from gnosh, which a running
    turn reads before its next request and which starts a turn in an
    idle session. When the call says nothing it waits, unlike Claude
    Code's main agent, whose calls go to the background by default: a
    local model handles a report it waited for better than one that
    comes later, and asking is one word. A sub-agent's own calls wait,
    as Claude Code's do. (A run, `-p`, made every call wait at first,
    since nothing read a notice that came after the run's turn; since
    item 36 a run waits for its background work, so it takes them as a
    session does.) A background sub-agent has no Agent tool; /tasks
    lists those running and stops one, and TaskStop stops one by its id.
32. **A hook's `if` filter takes what a deny rule of the same text
    would stop, and a call it is unsure of.** Claude Code's `if` is one
    permission rule; gnosh holds it against a call as it holds a deny
    rule: `Bash(rm *)` takes an `rm` anywhere in the line, inside a
    wrapper too, and a rule about files takes a command that reads or
    changes those files. A call part of which is only known when it runs
    (a program named by a variable) is taken, so a guard hook is never
    skipped for a call it was meant for; the price is a hook that now
    and then runs for a call it did not need to see. The session's own
    rules, mode and approvals play no part. A rule that cannot be
    applied lets the hook run for every call its matcher takes, and a
    filter on an event that is not about a tool call keeps its hook from
    running, as in Claude Code; the session says so of both when it
    starts.
33. **`--bare` is safe mode with less in it.** Claude Code's minimal
    mode skips the discovery of everything a person or a project set up
    and keeps Bash and the file tools. In gnosh it is safe mode (no
    hooks, also none from `--settings`; no context files, skills on disk,
    custom commands, MCP servers from files or the person's own prompt
    texts), the tools Bash, Read, Edit and Write unless `--tools` names
    others, none of the features (no mem, so neither its tool nor its
    lines on the prompt), and no background commands or sub-agents: a
    command whose time is up is stopped, as `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1`
    now also has it. What a run needs is given with flags.
34. **`/config` writes the state file, never a gnosh.json.** Claude
    Code's `/config` writes the user's own settings file, and a project's
    files win over it. gnosh never writes a gnosh.json (it is the
    person's, comments and all), so `/config key=value` keeps the value
    in the user's state file, beside the model chosen with `/model`: the
    layer above the user's gnosh.json and below a project's files, which
    is where Claude Code's choice sits. The value is checked by the very
    reader gnosh.json's `options` go through, and read back by it at the
    next start; one a later gnosh cannot use is left out with a warning
    and never stops startup. It is in force from gnosh's next start:
    most options are read once, as a session begins. The few a running
    session can change (autoCompactEnabled, autoCompactWindow, debug)
    are in force at once as well, where nothing above the state file
    sets them. `/config` alone
    lists every option with its value and what set it, `/config key`
    says more of one, and `key=` takes a choice away. Lists (instruction
    files, hidden skills) stay in gnosh.json.
35. **Hooks in the background run, as Claude Code runs them.** A hook
    with `"async": true` or `"asyncRewake": true` was a startup error,
    because a hook that is not waited for cannot stop anything (the
    principles' D3). In Claude Code such a hook is no guard either: it
    decides nothing, and what its answer adds for the model goes out
    with the next request; with `asyncRewake` an exit with 2 wakes the
    model, even in an idle session, with what the hook wrote to stderr.
    gnosh now does the same, so a Claude Code settings file with a lint
    or test hook in the background works unchanged. One that cannot be
    evaluated is a warning for the person, and nothing for the model.
    An `async` hook has no time limit and an `asyncRewake` one keeps its
    timeout; the session's end (and `-p`'s) stops those still running,
    with their process groups. With them came Claude Code's exec form
    (`"args"`), which runs the program with its arguments and no shell.
36. **A run waits for its background work, and asks instead of giving
    up.** Claude Code keeps a `-p` run open, once its turn has ended, for
    the background commands and sub-agents it started; the end of each
    wakes the model, and that turn is the run's too (text output prints
    each turn's reply, JSON reports the last). gnosh used to stop them
    when the turn ended, since nothing would read a notice that came
    after it (item 31's reason); now something does, and the Bash tool
    tells the model of a run that it will be told. Claude Code stops
    what is left after ten minutes of idle waiting. Jae's rule is that
    work does not end on a counter or a clock, so gnosh asks the model
    instead, every ten minutes of idle waiting: what still runs and for
    how long, stop it with TaskStop or end the turn to wait on.
    `CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS` still sets a hard ceiling for
    whoever wants one (`0`: no ceiling and no checkpoint). A run fed
    stream-json waits the same way once its input is over, and in
    stream-json output each turn's result line is written as the turn
    ends. A sub-agent the model starts in the background is waited for
    the same way, as Claude Code's run waits for its background
    sub-agents.
37. **http hooks run, and fail closed as command hooks do.** Claude
    Code's `"type": "http"` sends the event to a URL and reads the answer
    as a command hook's stdout; a status other than 2xx is a non-blocking
    error there. gnosh runs them the same way but holds them to its own
    rule for a hook that could not check (the principles' D1): no answer,
    a late one or one with another status than 2xx blocks where a hook
    can block. Headers use only the variables `allowedEnvVars` names, as
    in Claude Code; a header's value written in the file is kept as a
    secret; no redirect is followed. `allowedHttpHookUrls` and
    `httpHookAllowedEnvVars` are not adopted: the person's own files are
    where the hooks come from, and a folder's own are held until it is
    trusted.
38. **A session can run as a kind of agent.** `--agent <name>`, and the
    setting `"agent"` in gnosh.json, make the whole session one of the
    kinds the Agent tool knows (gnosh's own, the person's, the
    project's, `--agents`): its prompt stands in place of gnosh's own
    system prompt unless `--system-prompt` is given; the tools it does
    not list are off, put so as a deny rule of a tool's bare name in the
    command line's layer (and every MCP tool, unless it names some);
    its model is the session's unless `-m` names one; the instruction
    files still load. The session's file records the agent, and a
    resumed session runs as it again unless `--agent` names another. A
    definition that asks for what a session cannot have yet (hooks,
    skills, MCP servers, isolation, memory), a model that cannot be
    used, or a name that is no kind stops the start with the reason:
    it is never run as something else. /status names the agent.
39. **A sub-agent's report is scanned, and gnosh's tags are what it looks
    for first.** Claude Code puts a backslash in front of text in a
    report that imitates its harness (a `<system-reminder>` tag, a line
    that begins `Human:` or `Assistant:`) and a marker line in front of
    the report when it does, or when it names a permission-bypass
    setting. gnosh's harness speaks in `<gnosh>` and `<mem>` tags, so
    those are scanned for, with `<system-reminder>` beside them, the
    markers chat templates write a turn with (`<|im_start|>`,
    `<start_of_turn>`, `<｜User｜>`), and the two line openings. Each
    gets a backslash, which a model reads as quoting where the invisible
    mark Untrusted adds is read past. Lines that open with `User:` and a
    model's own `<think>` and `<tool_call>` are left alone: they come up
    in honest reports. The names scanned for are `bypassPermissions`,
    `--dangerously-skip-permissions` and `--yolo`, and they are left as
    they are. The caution is a note in square brackets at the head of
    the Agent call's result, which names what was found and says the
    report is data; a background sub-agent's notice carries it before
    the report. Nothing is taken out.
40. **A kind of agent preloads skills as text, and a missing one stops
    it.** A definition's `skills` gives the agent the whole text of each
    skill it names when it starts (after the instruction files, and again
    after a summary), and `effort` its thinking level, as in Claude Code;
    both hold for a sub-agent of the kind and for a session that runs as
    it, where `--effort` still wins. A preloaded skill is its text, with
    its !`command` lines run and no arguments: the permission rules, hooks
    and thinking level it carries come into force only when it is run
    with the Skill tool, since preloading is reading, not running. A
    skill the session does not have, one whose file keeps it from the
    model (`disable-model-invocation`), and one this gnosh cannot run stop
    the Agent call (or the session's start) with the reason: the agent
    is never run without instructions its author gave it.
41. **Monitor watches commands, at the pace the model reads.** The tool
    runs its command in the background, as Bash does with
    `run_in_background`, and passes each line on as a note from gnosh;
    the news that the command ended comes after its last lines, and a run
    (`-p`) waits for it as for any background work. Claude Code's form
    with `ws` (a WebSocket) is refused: gnosh has no WebSocket client.
    The lines are offered once a second, and while a note the model has
    not read yet waits in line, they are kept and offered again with what
    came after: a local model that is slow to read gets one note with
    many lines rather than a pile of notes. One note holds at most 100
    lines and 8 KiB, a line is cut at 1,000 bytes, and what does not fit
    is counted and left in the output file. Blank lines are not passed
    on. The time limit is the call's (`timeout_ms`, 5 minutes by
    default, at most 30), with no shorter one in a run. The command is
    judged as the same command in Bash is. A sub-agent has no Monitor:
    the lines go to the session the person is in.
42. **A kind's own hooks guard its own sub-agents, while each runs.** A
    definition's `hooks` block (YAML in a file, JSON in `--agents`) is
    put in force when a sub-agent of the kind starts, for that
    sub-agent's events alone: its tool calls, its permission requests,
    its SubagentStop (a Stop hook there is one, as in Claude Code). The
    session's own events and other sub-agents' are not touched, and the
    hooks go when the sub-agent ends. A session that runs as the kind
    (`--agent`) has them for the whole session, Stop as Stop. They are
    checked as the settings' hooks are: a block that cannot be read
    makes the definition unusable, and hooks that would not run stop the
    Agent call (or the session's start) with the reason. With every hook
    switched off (`disableAllHooks`, safe mode) there are none to put in
    force, as for a skill's hooks.
43. **A sub-agent's own work tree starts at HEAD, in .gnosh/worktrees.**
    `isolation: "worktree"` (on the Agent call, or in a kind's
    definition) gives the sub-agent a git work tree of its own on a new
    branch, `gnosh-agent-<id>`, named from the random end of its id, so
    that sub-agents started together never share one. It starts in the
    folder there that stands where the session's does, and the work tree
    is a working directory of the session from then on, as /add-dir
    makes one. It starts at the commit the repository has checked out,
    not at the default branch as Claude Code's does: the sub-agent is
    asked about the code as the person has it. What they have not
    committed is not in it, and the report says where its changes are.
    - **Removal:** a work tree is removed, branch and all, only when it
      is as it was made: nothing changed or new, even what git ignores,
      and its branch and HEAD still at the starting commit. It is removed
      without force, and the branch with git's safe delete. Any other is
      kept, and the report names its folder and branch, what it holds,
      and how to take it away.
    - **A failed attempt** to make one leaves no branch or folder behind.
    - **Where they live:** in the project's data folder, with a
      .gitignore that keeps them out of the repository's status.
    - **Permissions:** the rules judge what is inside a work tree as
      project files (`permission.Options.WorkTrees`), as they already
      did for Claude Code's `.claude/worktrees`. The folder of work trees
      and each work tree's own folder stay gnosh's, and a protected name
      inside a work tree is still protected.
    - **Links:** the folder of work trees must not be a link: gnosh makes
      none there, and the engine gives no exemption to one, nor to a data
      folder named like another protected one (`.git`). A repository can
      ship `.gnosh/worktrees -> ..`, which would otherwise have opened
      its `.git` to edits without a question. A review found this.
    - **Trust:** making one runs git, so it happens only in a trusted
      folder, as /diff does.
    - **--agent:** a session that runs as a kind that asks for isolation
      runs only with --worktree (47), which gives it the work tree of its
      own; without it, it is refused and told so.
    - **effort:** the Agent call also takes `effort` now, over its
      kind's.
    - **Its changes stay there:** the gate refuses a sub-agent's edit
      (Write, Edit, NotebookEdit) of a file in the person's checkout
      outside its work tree, whatever the rules and the mode allow, and
      names the same file in the work tree. Its system prompt says where
      it works (`agent-work-tree`), and a sub-agent it starts is held to
      the same work tree. Its commands (Bash, Monitor) are held to it as
      Claude Code holds them: one that would run in the person's
      checkout, by a cd in it or because the shell is there, is refused
      with the same folder in the work tree named, and so is one whose
      folder cannot be told from its text. So is git pointed at the
      checkout from the work tree (-C, --git-dir, --work-tree, GIT_DIR,
      GIT_WORK_TREE, GIT_COMMON_DIR, GIT_INDEX_FILE), or pointed where
      the text does not tell, and a command whose own name is only known
      when the line runs, which may be git. What a command writes there by its own
      paths ("cp a /repo/b") is left to the rules, as Claude Code leaves
      it. Box 1's Qwen showed the need: handed the
      checkout's absolute path in its task, it wrote there, and its own
      work tree, unchanged, was removed.
    - **A session in a kept work tree** works on its files as on any
      project's: the folder of work trees its project lies in counts as
      one of the engine's (`Options.WorkTrees` is a list), so the
      repository's .gnosh above them does not make them gnosh's own.
44. **The side panel takes the whole screen, and only when asked.** Jae
    asked for "a real side panel as an option" (15 November 2026). A
    panel the height of the screen cannot live in the terminal's
    scrollback, where whole rows scroll, so the panel is a second way of
    drawing the session (term.Panel). It takes the alternate screen and
    keeps what is committed itself, up to 10,000 rows. It draws the
    conversation and the live region at the left and the panel at the
    right, a third of the width (24 to 48 columns), in a terminal of 72
    columns or more. It comes on only by `/panel` (also `/sidebar`) or
    the `sidePanel` option, and `/panel` turns it off. The panel lists
    the files the session's edits changed, newest first with lines added
    and removed, then the deliverables, newest first. Each address is
    shown whole and is a link, so the boxes' addresses (127.0.0.1:7777,
    :7778) open on the Mac through the forwarded ports. While it is on,
    the terminal reports the mouse, so the wheel scrolls back (as
    PageUp and PageDown do), and text is selected with a modifier held.
    Turning it off, a narrowing below 72 columns, Ctrl+Z, the external
    editor and the end of the session all give the screen back. The
    lines committed meanwhile are then written into the scrollback,
    where they would have been, so nothing said is lost.

45. **gnosh ssh relays the far side's copies, and /copy uses this
    machine's clipboard.** Apple's Terminal drops OSC 52, the sequence by
    which a program on the far side of ssh puts text on the clipboard
    (gnosh's /copy, tmux's copy mode), so in it nothing copied on the
    far side ever reached the Mac. Jae asked for copying there to work
    three times. `gnosh ssh [ssh arguments...]` runs ssh with the
    terminal it was given and a pipe for its standard output, which gnosh
    writes on to the terminal as it comes, putting the text of each OSC
    52 write on this machine's clipboard as well (pbcopy on macOS;
    wl-copy, xclip or xsel on a Linux desktop). ssh keeps the rest: it
    reads what is typed, sets the terminal up, asks for passwords on it
    (so Apple's Terminal still sees a password prompt), writes its own
    messages wherever standard error goes, and stays in the shell's job,
    so Ctrl+Z stops it as it would; when it stops by itself (~^Z), gnosh
    stops the same way. A first version ran ssh on a pseudo-terminal of
    gnosh's own. A review of it (fifteen findings, all confirmed) found
    that this made ssh a session leader whose exit hung up its ProxyJump
    child and ControlPersist master, lost job control and the password
    prompt's protection, sent standard error to the screen, stopped
    `ssh ... &` on SIGTTOU, and could hang when the terminal went away;
    most of that went with the pseudo-terminal. Signals that end a
    program, sent to gnosh, are passed on (one ignored when gnosh
    started, as nohup's SIGHUP, stays ignored, for ssh too), and when a
    signal ends ssh gnosh ends by it as well, so a shell loop stops at
    Ctrl+C. The clipboard holds one text, so a copy that waits while
    another is made gives way to a newer one, and a clipboard that hangs
    never holds the screen up. Queries and clears are not acted on, nor
    are the 8-bit forms of the sequence, whose bytes are also parts of
    UTF-8 characters, and the clipboard is never read back; as in the
    terminals that take OSC 52, anything shown on the far side may put
    text on the clipboard. Without a terminal, with no clipboard tool, or
    when ssh is to go on in the background (-f, ForkAfterAuthentication),
    gnosh hands its process over to ssh (exec). main runs it
    before anything else is read, so every word after `ssh` is ssh's;
    the table has it for help (`-h` or `--help` alone shows that). A
    shell function in Apple's Terminal can send each interactive ssh
    through it. The same tools now serve `/copy` when gnosh was not
    reached over ssh (no SSH_CONNECTION, SSH_CLIENT or SSH_TTY); over
    ssh, where there is no tool, or when it fails, `/copy` goes through
    the terminal as before, and says so.
46. **mem keeps no secrets the agent hands it.** The text about mem says
    "Never keep a secret, or offer to", and on box 2 Qwen, with its
    thinking off, kept "the staging API key is sk-staging-9f3a2c" as a
    fact all the same; a new session read it back to the person. An
    input of the agent's (a gnovm_run call, a sub-agent's too) whose
    changes would keep text that looks like a secret, or whose code
    would (the journal keeps the code), is refused whole: nothing of it
    is kept, and the refusal says what it found, cut to its first five
    characters, and to keep only that the secret exists and where the
    person keeps it. What looks like a secret: a token that begins as
    services' secrets do (sk-, ghp_, github_pat_, glpat-, xoxb-, AKIA,
    AIza, hf_, dop_v1_ and the like) with 8 characters or more after it
    and a digit among them; a private key block; a JSON Web Token; a
    random-looking run of 32 characters or more mixing capitals, small
    letters and digits; and a secret's name (password, token, API key,
    secret, credential...) followed by "=", ":", "is" or "was" and a
    value that mixes letters with digits or signs, unless it is a path,
    a variable or a dotted name. A hash in hex, a UUID and a gno address
    are not secrets. The person's own inputs (/mem) are theirs to make,
    and a secret mem does not recognize is still kept until /mem redact
    takes it out, as GNOMEM.md shows.
47. **--worktree: a session in a git work tree of the repository's
    own,** as Claude Code's (its worktrees page). It is
    .gnosh/worktrees/<name> on the branch worktree-<name>. One of that
    name already there is opened again, and a branch of that name left
    from before is checked out again. Without a name one is made up
    (bright-river-3f2a). The session starts in the folder of the work
    tree that stands where it was started. The gate keeps its edits, and
    its sub-agents', out of the person's checkout, as for a sub-agent's
    work tree (43). Where gnosh differs, and why:
    - It is spelled out only. -w, Claude Code's letter, would let a
      mistyped -cwd be read as -c -w -d, against the table's rule that
      no flag's word is made of one-letter flags. A typed -w is told to
      use --worktree.
    - It starts at the commit checked out, not at the remote's default
      branch, which would need the network (as 43).
    - The folder must be trusted in print mode too, since making one
      runs git.
    - It is not started from inside another linked work tree.
    - At the end nobody is asked. A work tree left as it was made, not
      named and not there before, goes with its branch. Any other is
      kept, with a line that says how to come back to it (gnosh
      --worktree <name>) and how to take it away.
    The session's records are kept under the work tree's folder, so
    `gnosh --worktree <name> -c` goes on where it left off. The status
    line command is given Claude Code's worktree field and
    workspace.git_worktree. Not built: a pull request's head (#123) and
    worktree.baseRef. (.worktreeinclude is built: 79.)
48. **SendMessage gives a sub-agent that ended more to do,** as Claude
    Code's (spec/cc/subagents.md 9). The sub-agent's own session file is
    opened again as its history. It runs in the background under the
    same id, with the tools of its kind, and its report comes as a notice,
    as a background sub-agent's does. A report that can be followed up
    ends with `[agent id: <id>]`, so the model has the id. SendMessage
    never asks, as starting a sub-agent does not; its calls do.
    Refused: an id this session's sub-agents do not have; one still
    running; Explore and Plan, which are one-shot, as Claude Code's are;
    one that was stopped; and one that worked in a git work tree of its
    own (recorded in its file), since the work tree is not opened again.
    Only the session the person is in has the tool, and only where it may
    run sub-agents in the background.
49. **Ctrl+O is the transcript view, and detail is the verbose option.**
    Jae, 14 November: "i dont like how the thinking just disappears after
    two lines... what about ctrl-o or whatever to expand at least? or
    whatever claude does". Ctrl+O first turned detail on: the reasoning
    folded so far was written into the scrollback, and from then on
    reasoning and whole outputs as they came. A thought could be read
    again only until the next turn thought, which UI-22 asks more of.
    Claude Code's Ctrl+O is a transcript viewer, and its inline detail is
    the verbose setting; gnosh now has both the same way. Ctrl+O takes the
    whole screen (term.OpenView, a Panel with no side) and shows the
    conversation as the session's file holds it, rewinds applied: every
    prompt with what came with it, each reply with its time, its model and
    its reasoning, each call with its whole input and its result, notices,
    summaries and the steps begun and ended, the work of popped steps
    included. A thought still arriving shows at the end. Parts over 24
    lines are cut until Ctrl+E; Up, Down, PageUp, PageDown, Home, End and
    the wheel move; { and } go between prompts (Claude Code's keys); q,
    Esc, Ctrl+C or Ctrl+O go back. The session goes on meanwhile: the view
    takes in what is written to the file, and what the session said is
    written into the scrollback when the view closes, as the side panel's
    is. A question closes the view, so that it is seen. The verbose option
    (Claude Code's key, under "options") starts the session with detail
    on; the store of folded thoughts and Ctrl+O's writing them out are
    gone.
50. **/model with nothing after it is a picker.** Jae, 16 November, on
    box 1: "the modal doesn't seem to pop up when i type ssh ai and
    /model". The command printed the models as text; Claude Code's and
    the earlier gnosh's open a picker. In the interface it is now a
    list of the models the providers' servers have (asked away from the
    screen's loop, for fifteen seconds at most), the one in use marked
    with its window and thinking and the cursor on it. Enter on a model
    with thinking levels then asks which, the cursor on the level a
    switch with none would take (the one saved for it, else the default,
    as the command takes it), so that Enter twice never changes more than
    the model; Esc keeps the model as it is. What is picked runs as
    /model provider/model level, which switches and saves as typing it
    does; /model with a name runs as before, and in print mode and the
    plain front end /model with nothing still lists the models as text.
51. **/config with nothing after it is a picker too.** After /model (50),
    the next bare command that chooses something: Claude Code's /config
    opens its settings. gnosh's lists its options in a menu, each with its
    value and where that comes from. Enter on an option that is true or
    false turns it over, one that takes one of a few words asks which
    (the cursor on the one in force), and any other asks for its value
    (the one set, to begin with; nothing typed takes back what /config
    set). The change runs as /config key=value, quoted where it holds a
    blank, which keeps it for every project as typing it does; a list is
    left to gnosh.json. /config with a key, print mode and the plain
    front end are as they were. /export with nothing after it asks the
    same way, as Claude Code's does: the clipboard (/export --clipboard,
    which goes as /copy goes, through gnosh ssh's relay to the Mac too)
    or a file here named for the day and the session. /memory with
    nothing after it lists the context files, those the model is given
    and then the person's own and the project's GNOSH.md (marked when not
    there yet), and opens the one picked in the person's editor in place,
    as Claude Code's opens its memory files; a change shows in the next
    session. /mcp with nothing after it lists the MCP servers with how
    each stands, and Enter on one offers what fits it (reconnect it or
    disable it for the session, or enable one that is disabled), which
    runs as the /mcp command; with no server it says how to add one, as
    before.
52. **Alt+T turns thinking over, Alt+P switches the model, and /effort
    with nothing after it is a picker,** as Claude Code has them. On the
    AI boxes Qwen answers fast with thinking off and better with it on,
    and a key is the quick way between them. Alt+T is the command
    /effort with the next level: off and on of a model with that switch
    (one that nothing chose thinks, so the first press turns it off), the
    next level of a dial, after the last the first; it is kept for later
    sessions, as /effort keeps it. Alt+P opens the /model picker and
    leaves what is typed in the box. /effort with nothing after it lists
    the model's levels and auto, the one in force marked. In Apple's
    Terminal the Alt keys need Option set to act as Meta.
53. **/permissions adds and takes out rules, and with nothing after it is
    a picker,** as Claude Code's /permissions is. /permissions allow, ask
    or deny and a rule puts it in force at once and keeps it in the
    project's local settings file (.gnosh/settings.local.json, never
    committed, where an approval kept at a prompt goes); /permissions
    remove and a rule takes it out of force and out of that file. gnosh
    writes that file as plain JSON, as Claude Code does its own, so a
    rule in the person's own gnosh.json or the project's, which may hold
    comments a rewrite would lose, is named with its file and left. A
    rule gnosh would not load is not written. With nothing after it,
    /permissions lists the rules in force under three rows a rule is
    typed into (allow, ask, deny); picking a rule asks before it is taken
    out. In a run, /permissions still prints the listing.
54. **skillOverrides, and /skills sets it,** as Claude Code has them. The
    settings key maps a skill's name to how far it is seen: on (as its
    file says), name-only (the model is told its name and not its
    description), user-invocable-only (the person's alone, as
    disable-model-invocation) or off (nobody may start it, and the menu
    leaves it out; /skills still lists it, so it can be turned back on).
    A later file wins for each name. Each value only takes away, so a
    folder that is not trusted keeps its own. /skills <name> <how> sets
    it for the rest of the session and keeps it in the project's local
    settings file, which gnosh writes; /skills with nothing after it is a
    picker of the skills, Enter on one asking how far. gnosh's own
    hiddenSkills option stays beside it.
55. **A settings file changed while the session runs is taken in,** as
    Claude Code takes in a change to its settings files: before each
    request to the model, the settings files (the person's gnosh.json,
    the project's, the local file, a --settings file) are looked at, and
    when one changed the configuration is read again. The hosts and the
    files' permission rules go in force at once; the model is told when
    the hosts changed; the person is told what took effect and that the
    rest (hooks, env, options, the model) waits for a new session. A file
    gnosh would not load leaves the session as it was, and the person is
    told why. Box 1, 16 November: a host added to gnosh.json mid-session
    was no host the session knew, and the model, told so, tried ssh
    spelled so as to slip past the refusal.
56. **Vim's keys, as Claude Code's editorMode "vim",** with what its
    documentation lists (modes, motions, operators, text objects, VISUAL
    mode, "." and undo) and counts, which it does not list and vim users
    type without thinking. editorMode and vimInsertModeRemaps are options
    under "options", as gnosh keeps the Claude Code settings it built
    that are not permission rules, hooks or env; a top-level key is
    pointed there. Neither is taken from a project's file: Claude Code
    leaves a project's remaps out so that a repository cannot remap the
    person's keys, and the same reason holds for the mode. /config
    switches the mode at once. A sent or cleared box starts again in
    INSERT mode, ready for the next prompt. "." types again the text of an
    INSERT session, never a history step, a submit or a paste from the
    ring, which a replay must not do twice.
57. **Output styles, as Claude Code's outputStyle and /output-style.**
    gnosh has Claude Code's four besides the default (Proactive, Concise,
    Explanatory, Learning), each written in gnosh's words from what
    Claude Code's documentation says the style does, and a person's own
    or a trusted project's Markdown files in an output-styles folder
    beside their skills, the closest first. The style is sent right after
    gnosh's core, the one choice of the person's that changes the system
    prompt. A style of one's own without keep-coding-instructions puts a
    plain role in place of the core's first line (the coding agent) and
    keeps the rest, which is how to work in gnosh, not how to code: gnosh
    has no software-engineering section to leave out. /output-style keeps
    the choice in the project's local settings file, as Claude Code
    does, which a folder that is not trusted does not read: the choice
    then holds for the session, and the command says so. A name no style
    has is said at the start, and the default used.
58. **A subshell is the commands in it.** A line holding ( ... ) used to
    ask whatever was inside; now each command in it is judged, as Claude
    Code judges every subcommand, and a cd inside moves the subshell
    alone: where the line stands is put back when the subshell ends
    (cmdSpan, lineState.leave). Nothing else a subshell changes can
    reach the commands after it unasked, since setting a variable or the
    shell's settings makes any line of more than one part ask. Brace
    groups (since the next commit) are read straight through, as what
    runs in the shell itself, unless a redirection follows the group,
    which is opened before any cd in it. if, while, until and case
    (since the commit after that) are read straight through as well,
    every branch judged, in a line that changes no directory: where a
    command runs then does not hang on which branch ran, and with a cd,
    pushd, popd or a computed program in the line they still ask. A for
    loop over a list of words written out is read too, its body judged
    once for each value (filledArgs); a list that holds a pattern or
    anything computed still asks, since a pattern's file names may begin
    with a dash and be flags where the body puts them (the oracle's
    attack lines found "for f in *; do cp a.txt "$f"x; done" running
    cp a.txt -Rx on the first try). Functions, for ((...)), a loop over
    a script's words and (( )) still ask.
    Replaying the 2,180 commands
    Qwen ran on the boxes, 36 asks were these forms, the commonest
    "(go test ./... 2>&1 || true)" and "(cd dir && ...)".
59. **WebSearch asks a SearXNG server the person names, or is not there.**
    Claude Code's WebSearch runs on Anthropic's backend; gnosh keeps the
    tool's name and inputs (query, allowed_domains, blocked_domains, mode)
    and asks the server the webSearchUrl option names, with its JSON
    format (/search?q=...&format=json). A self-hosted SearXNG keeps the
    queries on the person's own machines, and needs no key and no
    scraping of a search engine's pages. Without the option the session
    has no WebSearch tool; a rule may still name it. It asks before it
    runs, as Claude Code's does, and lists titles, addresses and a line of
    each result; pages are read with WebFetch.
60. **/restart starts gnosh again in the session, from the program file
    there is now** (Claude Code's /restart, also /update). gnosh is
    installed by rename while sessions run, and a long session kept the
    build it started with. The session closes as /exit closes it, and the
    process becomes gnosh started anew (exec) with its own flags written
    out whole, --resume=<the session> in place of -c, -r, --session-id,
    --fork-session and -n, no prompt words, and the permission mode, the
    model and the thinking level as they stand; a work tree is kept and
    named. It waits while a turn or background work runs, since closing
    would stop them. A changed program file (another file, time or size
    than gnosh started from) is said once, as a turn ends. The program
    file is the name gnosh was run by, found on the PATH, so that an
    install behind a link counts.
61. **keybindings.json, as Claude Code's, for the contexts gnosh has.** The
    file is ~/.config/gnosh/keybindings.json, in Claude Code's form. A key
    bound to an action is taken as the key gnosh already knows that action
    by, and goes to the handler that takes that key; null takes a key
    away; command:<name> runs /<name>. The contexts are Global, Chat (Task
    too) and Transcript, with 21 actions, and chords of two keys: the
    first is held up to 3 s for the second, and goes through as itself
    when the pair is no chord of the person's. The other contexts and
    keys that always do what they do (Ctrl+C, Ctrl+D) are said and left
    out. While a question is open no key is changed, so an answer is given
    with the keys the question shows. The file is looked at again at most
    once a second as keys come; /keybindings opens it in the editor, made
    with nothing bound when it is not there.
62. **/background (also /bg) hands the session to a run of its own.**
    Claude Code's detaches a live session to its agent view; gnosh has no
    such server, and two processes may not write one session. So the
    session ends here as /exit ends it, and gnosh starts, in a process
    of its own (setsid) that outlives the terminal, as gnosh -p
    --resume=<the session> with the session's flags as they stand and the
    prompt given after --. The run asks nobody, as every run, and its
    output goes to <session>.background.log beside the session's file;
    gnosh -r opens the session again once it is done. A prompt is
    needed, and it waits while a turn or background work runs, as
    /restart does.
63. **/fork <prompt> starts a copy of the conversation as a run of its
    own, and this session goes on.** The copy is /branch's (a new session
    of the folder, named "<title> (fork)"), started at once as /background
    starts a run: in a process of its own, with this session's flags as
    they stand, on the prompt. Claude Code's fork may make itself a work
    tree; gnosh's works in the same folder, and says so, since a fresh
    tree would hold none of the changes the conversation remembers making.
64. **A session opened again shows where it left off.** Claude Code draws
    a resumed session's conversation; gnosh's interface drew only its
    header, so a session that a /background or /fork run had worked on
    showed nothing of what it did. Now its last prompt and the model's
    last answer to it are drawn (at most 30 lines of the answer; Ctrl+O
    shows everything), under a quiet line saying so: on -r and -c and on
    /resume. Not after a rewind or /branch, nor in the gnosh /restart
    starts, whose conversation is still on the screen: /restart says so
    with GNOSH_RESTARTED=1, which gnosh takes out of its environment as it
    starts.
65. **PreModelSwitch, as Claude Code's.** /model (and its picker, which
    runs /model) runs the PreModelSwitch hooks once the model is found
    and before it is switched to. exit 2, "decision": "block" or
    "permissionDecision": "deny" keeps the model as it was and says why;
    "ask" puts the switch to the person, and is a refusal in a run, where
    nobody can answer; a hook that could not check refuses, as every
    gnosh gate does. The hooks read from_model, to_model, requested_model,
    source ("command") and context_tokens (the conversation's size at the
    last count). Claude Code's price fields (prompt_cache_warm, cache_ttl,
    estimated_cache_write_usd, pricing) are left out: gnosh knows no
    prices and no cache lifetimes for the models it talks to. Claude
    Code matches a model's canonical name; gnosh matches the model as
    provider/model and by its own name, so "qwen3.8-27b" fits
    qwen/qwen3.8-27b. PreModelSwitch and PostModelSwitch hooks get 30
    seconds by default, as in Claude Code.
66. **What goes on with a session is told what it cannot find itself.**
    /restart, /background and /fork start gnosh with the session's flags
    (cli.RelaunchArgs). A session that has written nothing yet has no file
    to resume, so it is started again under its id (--session-id, with
    the name -n gave it). A model picked from the list as the session
    started is kept nowhere, so it is passed on (--model); a model from
    the settings, or saved by /model, is found there again. A session
    that started in bypassPermissions by its name, as after -y, can go
    back to it; /effort auto drops --reasoning-effort. A work tree a
    /fork run was started in is kept when the session ends, and /resume
    refuses a session another gnosh writes before it closes this one.
67. **ConfigChange, as Claude Code's, for the files gnosh watches.**
    Before each request gnosh looks at its settings files; when one has
    changed, the ConfigChange hooks run for it, with source
    (user_settings, project_settings or local_settings) and file_path,
    before anything is taken in. A block (exit 2, "decision": "block", or
    a hook that could not check) keeps all the settings the session had,
    and, unlike Claude Code, which only logs it, the person is told. A
    file named by --settings has no source in Claude Code and runs no
    hook. gnosh has no managed settings and does not watch skill files,
    so policy_settings and skills never fire, and a matcher on them is
    warned about as fitting nothing. gnosh's own writes (saved approvals,
    /permissions) are no change, as in Claude Code.
68. **PostToolBatch, as Claude Code's.** The agent loop has a moment of
    its own for it (Moments.AfterRound): once every tool call of a reply
    has finished and its result is stored, when the model is about to be
    asked again. Not when the round ends the turn by itself (a halt, a
    stop): there is no next request to come before. The hooks read
    tool_calls, each with tool_name, tool_input, tool_use_id and
    tool_response, the text the model reads of the result (Claude Code
    passes the tool_result content as sent, a string or blocks; gnosh's
    is always the text). additionalContext goes to the model after the
    results; a block, "continue": false or a hook that could not check
    ends the turn there, and the reason is kept in the conversation, so
    the model reads it when the session goes on. Sub-agents' batches run
    the hooks too, with agent_id and agent_type.
69. **FileChanged, as Claude Code's, by looking twice a second.** Claude
    Code watches files with the system's file events; gnosh, which uses
    the standard library alone, looks at the watched files every 500 ms
    (a stat each), and only while a FileChanged hook is configured. The
    files are the ones the FileChanged matchers name (split on "|", each
    a file name in the folder the session started in; a group with no
    matcher adds none and runs for every watched file) and the last
    watchPaths a SessionStart, CwdChanged or FileChanged hook gave (an
    empty list clears them; only absolute paths are taken). A file made,
    changed or removed runs the hooks with file_path and event (add,
    change, unlink); the first look at a file only notes how it stands.
    The hooks get the environment file (GNOSH_ENV_FILE, CLAUDE_ENV_FILE)
    as CwdChanged's do, and their systemMessage is shown. Matchers are
    Claude Code's narrow kind: only letters, digits, _ and | make exact
    names, so ".envrc" is a pattern, as there.
70. **A PostToolUse hook may replace what the model reads of a tool's
    output (updatedToolOutput, updatedMCPToolOutput), as in Claude Code;
    one gnosh cannot read hides it.** The replacement is read as the text
    the model reads: a string, an object with "text" (the shape gnosh
    gives hooks), Claude Code's Bash shape (stdout and stderr), an object
    whose "content" is a string, or MCP's content blocks. Claude Code
    ignores a value that does not fit the tool's shape and sends the
    original; gnosh withholds the output instead, and does the same when
    two hooks give different replacements, because such a hook is
    usually there to keep something (a key, say) out of the
    conversation. The call has run all the same, and the replacement is
    what the session keeps.
71. **Prompt suggestions, as Claude Code's, off unless set.** The ruling
    of 13 November had them among what is not adopted; the next-prompt
    kind is adopted now, under Claude Code's key, promptSuggestionEnabled
    (also in /config, at once). After a turn that ended as it should, in a
    session a person attends, out of plan mode and with nothing queued,
    the session's model is asked beside the session, keeping nothing, for
    the prompt the person would most likely send next (at most 64 tokens;
    the first line is kept, at most 200 characters). The empty box shows
    it; Tab or the right arrow takes it, anything else drops it, and a
    prompt sent stops one still being made, so the prompt's own request
    never waits behind it. Off by default, as Claude Code has it off for
    models of other providers (it is on for its own): each is a request to
    the model, which on a local box is the same GPU the next prompt wants.
    Not from a project's file, since a project should not spend the
    person's requests. Claude Code's git-history example in a new
    session's box is not adopted.
72. **Invisible characters are taken out of a prompt on Enter, as in
    Claude Code.** Text copied from a page or a message can carry
    characters a terminal draws as nothing, and with them words or an
    order of words the person never saw. As Enter sends a prompt (and
    the prompt the command line gave), text.CleanInvisible takes out tag
    characters (all but the three subdivision flags emoji have, England,
    Scotland and Wales: any other run of tags behind a black flag is
    hidden text), the controls and marks of the drawing order, zero-width
    spaces, the soft hyphen, the old format controls, the combining
    grapheme joiner, the Hangul fillers, the interlinear and musical
    format controls, joiners except between two characters beyond ASCII
    (Persian, the scripts of India, emoji), and variation selectors
    except one after a symbol, a mark of punctuation or a Han character,
    a keycap's, or a Han variant's. The whole prompt is cleaned at once,
    so a character beside a paste is judged with its neighbours. Claude
    Code sometimes sends the cleaned prompt and sometimes puts it back;
    its docs do not say when, so gnosh's rule is: it is put back, with
    nothing sent, when what was taken out could hide text or change its
    order (tag characters, the order controls, three or more in a row,
    the way hidden bytes are spelled), when what is left begins as a
    command or a shell line, or is the word that leaves, and what was
    typed did not, and always for a shell line, whose command what was
    taken out changes; otherwise it is sent and the person told. Bytes
    that are not UTF-8 are read as the replacement character first, so
    taking a character out never joins two pieces into a new one. Print
    mode sends its prompt as given.
73. **A model on a llama.cpp server thinks at most 4,096 tokens before a
    reply, unless told otherwise.** On Jae's AI box, with thinking on,
    Qwen thought for 14,270 tokens (31 minutes) before one reply, and
    seven turns before it were stopped by hand with no reply at all; the
    server had no budget, and gnosh left the budget to its start-up flag.
    llama.cpp's server takes one in each request (reasoning_budget_tokens,
    from build b9982; the box runs b11714) and puts a message at the end
    of the thinking when it runs out (reasoning_budget_message): gnosh
    sends the budget whenever the model may think, with its own note
    (thinking-budget.md). The budget is MAX_THINKING_TOKENS, Claude Code's
    variable, when it is set above 0; then the model's own thinkingBudget
    in gnosh.json (0 for no cap, not from a project's file unless the
    folder is trusted, like the model's other fields); then 4,096 for a
    llama.cpp server, some minutes at the speed of a model on the
    person's own machine and more than an ordinary step takes; and none
    elsewhere. MAX_THINKING_TOKENS=0 turns thinking off, as in Claude
    Code. A server too old for the fields ignores them.
74. **The theme option, as Claude Code's /theme, with gnosh's colours.**
    Claude Code offers auto, light and dark, daltonized and ANSI themes
    and custom ones. gnosh has its own palette (Jae's colours,
    DECISIONS of 14 and 16 November in the UI notes) and offers four
    ways to take it: auto (for the background the terminal says it has,
    as before), dark and light (whatever it says: over ssh or in tmux a
    terminal often says nothing), and ansi (the terminal's own sixteen
    colours, which its owner chose). Bare /theme opens the option's
    picker; /theme <name> and /config theme=<name> set it for every
    project and at once. No daltonized or custom themes: gnosh never
    tells a thing by colour alone.
75. **A reply printed to a terminal may colour itself and do nothing
    else, as Claude Code 2.1.325 has it for claude -p.** In print mode
    to a terminal, the reply and gnosh's lines on standard error keep
    their styles (SGR), tabs and line breaks; every other escape
    sequence and control character, the C1 controls included, is left
    out (text.Safe; text.SafeStream across the pieces of a reply). A
    pipe or a file gets the reply as it is: it is data there, and the
    reader decides what to do with it.

76. **git in a session's own work tree leaves alone what the work tree
    shares with its checkout, as Claude Code 2.1.326 has it for its
    sub-agents.** A work tree shares its branches, tags and other refs,
    its git settings and hooks, and its list of work trees with the
    checkout it was made from. git keeps a branch another work tree has
    out from checkout and branch -f, and from little else; a setting such
    as core.fsmonitor, core.hooksPath or an alias runs a command in the
    checkout. A first version refused a list of known-bad forms, and an
    independent review (17 November) moved the checkout's branch on real
    git with about twenty lines it let through: subcommands not on the
    list, -c settings, --global, the person's own aliases, git's
    --exec-like options, the environment. So it is an allowlist now
    (permission.Engine.SharedGitChange). In a work tree, whatever the
    permission mode, git may: read (the read-only table, or a reading
    subcommand with no flag that writes a file or starts a program); and
    run add, commit, reset, restore, rm, mv, checkout, switch, merge,
    rebase, cherry-pick, revert, stash push and apply, fetch, pull, a push
    to a remote, branch and tag to make one, clean and apply, each with
    the flags gnosh knows it by, spelled out whole. Anything else is
    refused: other subcommands and aliases, -c settings other than a
    short list (user.*, color.*, ...), git's options before the
    subcommand other than -C and a few, GIT_ variables other than a short
    list, HOME, XDG_CONFIG_HOME and PATH where git runs, an editor or
    pager that is not true, : or cat, a word only known when the line
    runs where git would take it for an option (put -- before it), a
    refspec that writes outside refs/remotes/, a push into a folder on
    this machine, rebase given a second branch, a merge strategy git does
    not have, stash pop, drop and clear, and on a Mac a new branch with
    capital letters (on a disk that does not tell case apart, MAIN is
    main). A file a command writes by a computed name is refused in a
    work tree as one gnosh cannot tell.
    A second review (17 November) found the next layer: the text says
    where git runs, not which repository it opens, and a .git file can
    name the checkout's. So git in a work tree runs only against the
    work tree's own repository, which gnosh tells from the disk at the
    moment of the call (app treegit.go: the first .git above each folder
    git runs in, a .git file followed to the folder it names); git -C to
    another repository, --git-dir and --work-tree are refused; and no
    command, Write or Edit may change a .git file or folder in a work
    tree. Also: variables set every way bash has (a VAR=value word of
    env, sudo or time, read, printf -v, a nameref, a for loop's
    variable, ${VAR:=x}); push only to a remote the repository has, by
    its name, or to an address, and -u only for HEAD; fetch without
    --filter and --depth, which write files the checkout shares; on a
    Mac, no capital letters in a branch or tag switched to or made;
    grep's -O inside a cluster and starts of long flags (--open); and
    patterns with a quoted part. Reading commands take a quoted word
    computed when the line runs ("$(git merge-base HEAD main)"). What
    the text and the disk cannot show stays out of reach: a git copied
    under another name, a script or a program that runs git, a remote of
    the person's whose address is the checkout, and the one-line forms
    that make a .git and use it at once. Real isolation would take a
    sandbox of the operating system. Claude Code holds only sub-agents
    to this; gnosh holds a --worktree session to it too, as it holds one
    to its tree's files.
77. **/cd moves a session to another folder by starting gnosh again
    there.** Claude Code's /cd keeps the conversation and applies the
    new folder's project settings, permission rules, hooks, MCP
    servers, skills, agents and instruction files as it moves. gnosh
    already does all of that one way, as it starts in a folder, so /cd
    starts gnosh again there, as /restart does (--cwd names the folder,
    --resume the session, and /add-dir's folders go along as --add-dir).
    Before anything moves: the Cd rules are checked (Claude Code's: a
    bare deny switches /cd off, a deny keeps it out of the folders it
    covers, as typed, by way of each link and as resolved, and any allow
    rule makes a list of the only folders it may go to; patterns are
    matched against the whole path, * one segment, ** any number; there
    are no ask rules); a folder not trusted is asked about in the
    session, and a no leaves the session where it is; and the folder's
    configuration is read, so that the new start does not stop on a
    mistake in it. Then the session's file goes with the new folder's
    sessions (a meta entry "moved" records the folder, which the list
    of a folder's sessions goes by), its own folder (its commands'
    output files, its sub-agents) goes along, and the gnosh started
    there tells the model where it now works and runs the CwdChanged
    hooks. mem is a project's, so it is the new folder's from then on:
    what it held for the session stays with the folder it left. /cd
    waits while a turn or background work runs, is not for a run, and
    does not leave a session's own work tree.

78. **/hooks is a browser that changes nothing, as Claude Code's is.**
    Bare /hooks lists the moments that hooks run at, with each moment's
    hook (or how many it has) and a note on any hooks that are configured
    but not in force. Enter on a moment lists its hooks, each with the
    file it is written in. Enter on a hook shows all of it: what it runs,
    what it matches, its if filter, its timeout, how it runs, and its file.
    Back goes up a level and Esc closes the browser. A hook is changed in
    its settings file: gnosh writes JSON settings for /permissions and
    /skills, but a hook names a program to run, so the browser does not
    add one. With no hook in force, and in a run, /hooks still says in
    text what it said before.

79. **A new work tree gets copies of what .worktreeinclude names, as
    Claude Code's does.** A checkout has none of what git ignores, such
    as a .env or a local settings file. A repository's .worktreeinclude
    (in .gitignore's syntax, at its top) names the files of the person's
    folder that a new work tree gets a copy of, a --worktree session's or
    a sub-agent's: those it names that git ignores, each in its place,
    found by git itself (ls-files with the file as its exclude list, then
    check-ignore). A file the tree has already is left alone, a link is
    copied as a link, and no copy goes where a link in the tree's own
    checkout would carry it out of the tree. A copy left as it was is no
    work of the tree's: one whose only difference from the person's
    folder is such copies is removed at its end like any other left as
    made. The session's first line names the copies; a sub-agent's are in
    the log; what could not be copied is said.

80. **EnterWorktree and ExitWorktree move a running session into a work
    tree of its own and back, as Claude Code's tools do, by starting
    gnosh again.** Asked to work in a worktree, the model calls
    EnterWorktree, with a name or without one (a name is then made up).
    gnosh makes the tree as --worktree does, from the commit checked out
    on the branch worktree-<name>, or opens one of that name again. The
    session's file goes with the tree's sessions, and gnosh starts again
    with --worktree <name> and --resume, as /cd moves a session (77): the
    tree's settings, instructions and the gate that keeps edits in it
    are then in force by the one way gnosh has of taking them in. The
    call ends the turn; the gnosh that starts is told what moved
    (GNOSH_TREE_MOVE) and goes on with the task in a turn of its own.
    ExitWorktree goes back to the folder the session started in, with
    keep (the tree and its branch stay) or remove (both go). A tree that
    holds work is removed only with discard_changes, which the model is
    told to ask the person about. A tree EnterWorktree made and nobody
    named goes at the session's end when it was left as made, as an
    unnamed --worktree tree does. The tools are an interactive
    session's, in a trusted repository, in a gnosh that can start itself
    again; neither asks first, as Claude Code's do not for a new tree.
    They refuse while work runs in the background or the person's
    prompts wait, which the restart would lose. Unlike Claude Code's, a
    tree is named, never given as a path: a work tree made with git
    worktree add is worked in by starting gnosh there. ExitWorktree that
    removes a tree with discard_changes (its work is lost) asks the
    person first, whatever the mode, as Claude Code's does since
    2.1.327; without it, a tree that holds work is not removed.

81. **Scheduled prompts, as Claude Code's: CronCreate, CronList,
    CronDelete, and /loop with an interval.** A session can schedule a
    prompt to be sent later, on a five-field cron schedule in local time
    (internal/cron, standard library only: lists, ranges, steps, 0 or 7
    for Sunday, either day field matching when both are limited), once
    or recurring. /loop 5m <prompt> (or <prompt> every 2 hours) schedules
    its prompt with the interval rounded to a step cron can take, and
    sends it at once. A prompt that comes due goes in between turns,
    when the session is idle with nothing of the person's waiting, once
    however many of its times passed meanwhile; a line says which task
    runs. A recurring task goes after 7 days, after one last run; a
    one-shot task goes when it has run; a session holds 50. Bare /loop
    keeps its meaning (keep the session working on its task). The tools
    are an attended session's, quiet in the permission engine, and
    CLAUDE_CODE_DISABLE_CRON=1 takes them and the interval /loop away.
    /loop <prompt> with no interval runs it now and again when the model
    says: at the end of each run it calls ScheduleWakeup with a delay
    from one minute to an hour, or with stop when the task is done; a run
    that schedules nothing gets one more, 20 minutes on, and the loop
    ends when that one schedules nothing either; /noloop ends it. The
    tasks are kept in the session's file (a meta entry), and --resume or
    --continue brings back the recurring ones younger than 7 days, from
    their next time on, and the one-shot ones whose time has not come; a
    self-paced loop is not brought back. Not taken: the jitter Claude
    Code adds to spread its fleet's requests, which a local model server
    does not need.

82. **Channels, as Claude Code's: MCP servers that push events into a
    running session.** spec/cc/cli.md had listed --channels as
    Anthropic-only, since Claude Code ties it to its plugin marketplace,
    its approved list and its own sign-in. The protocol underneath is
    plain MCP (its channels-reference page): a stdio server declares the
    experimental capability claude/channel and sends
    notifications/claude/channel with content and string meta. gnosh
    takes it for servers of the session's own MCP configuration, named
    --channels server:<name> (--dangerously-load-development-channels is
    the same flag: there is no list of approved channels to step past,
    and no plugins). Each event is put in front of the model as a
    <channel source="name" key="value"> block (meta keys that are not
    identifiers dropped, as Claude Code drops them, and so are source
    and role), waking an idle session, and the person sees "← name:
    content". Each block comes after a line of gnosh's own saying it is
    an outside source's words, never the user's, so that the word
    survives a summary; the content has &, < and > written as entities,
    so nothing in it closes the block or reads as gnosh's tag, and is
    cut at 16 KiB. A channel pushes 30 events a minute at most: the rest
    of the minute's are dropped, and the person is told once. A server
    that does not declare itself a channel is not heard, nor is one
    reached at a URL (a channel runs on this machine), and a channel's
    instructions are put in front of the model as it connects. The use:
    a CI webhook or a chat bridge reaching the session on an always-on
    box. Relaying permission questions to a channel is item 85.

83. **PushNotification, as Claude Code's: the model calls the person
    back.** The tool sends one line (under 200 characters, blanks folded)
    the ways gnosh already tells a person who is away that the session
    waits: a desktop note through the terminal by preferredNotifChannel
    (OSC 9 or 99, or the bell), and the Notification hooks, with the
    notification type push_notification (gnosh's own; Claude Code's
    reaches a phone through Remote Control, and a hook that forwards
    this type to a phone service is gnosh's way). When a key was pressed
    in the last half minute the person is at the terminal and nothing is
    sent, as in Claude Code, and the model is told so; the plain
    interface sends none. One is sent a minute at most, and the text
    loses its control characters (a terminal acts on an escape or a
    bell) and the marks that turn text around. Offered in attended
    sessions, quiet in the permission engine.

84. **Focus view, as Claude Code's /focus, in the scrollback.** Claude
    Code's focus view (fullscreen rendering only) shows the last prompt,
    a one-line summary of the tool calls with edit diffstats, and the
    final response; /focus turns it over and the choice persists, and
    the viewMode setting (default, verbose, focus) starts a session in
    one view, winning over verbose. gnosh has the same command and
    option (viewMode is kept in the state file, not from a project's
    file). gnosh writes into the terminal's scrollback, where nothing
    written can be taken back, so its focus view folds what can be
    held: each run of tool calls becomes one line when something else is
    written ("Read 3 files, searched for 1 pattern, edited 2 files +40
    -12, ran 4 commands · 1 failed · Ctrl+O shows them"), and the
    model's thinking time goes into that line. The model's words between
    calls stay where they were written. A call in flight shows in the
    live region under a "so far" line, questions show as always, and a
    deliverable stands on its own. Ctrl+O's transcript view shows every
    call whole either way.

85. **Permission questions relayed to a channel, as Claude Code's.** A
    channel named in --channels whose server declares
    claude/channel/permission besides claude/channel is sent each
    question about a tool call as it opens
    (notifications/claude/channel/permission_request: a request id of
    five lowercase letters without l, the tool's name, what the call
    does, and its input as display text), and its verdict
    (notifications/claude/channel/permission, allow or deny, with the id)
    answers the question that stays open at the terminal; the first
    answer counts. Deny is the terminal's bare no: the call does not run
    and the turn stops. The text sent is made fit as Claude Code makes
    it: characters that turn text around or cannot be seen left out,
    quote and bracket lookalikes written plainly, blanks folded, values
    over 3,500 characters kept by their start and end around a count of
    what was left out, and the tokens services hand out as credentials
    sent as [REDACTED] (never a span with a path's, a URL's or a shell's
    characters). Where gnosh differs: a request id is never issued twice
    in a session (Claude Code drops verdicts for an id that came up
    twice), and plan approvals stay at the terminal, as trust questions
    do in both. The person is told as the channel connects that whoever
    answers through it can allow or refuse calls, and each answer from it
    is said on the screen.

86. **@ mentions search the whole project, as Claude Code's.** gnosh
    completed a mention a folder at a time, by the start of a name;
    Claude Code's @ finds a file anywhere in the project by what was
    typed. Now, after the names in the session's folder that begin with
    what follows the @, the project's files and folders whose paths hold
    it are offered, best first: a name that begins with it, one that
    holds it, a path that holds it, and a path that holds its letters in
    order, the fewer pieces the better. The project is listed in the
    background, by git in a repository (tracked files and the untracked
    ones it does not ignore) and by a walk by the ignore rules elsewhere,
    at most 50,000 paths in 3 seconds; a list is used for half a minute
    and listed again after each turn. Hidden paths are left out, as the
    folder-at-a-time completion leaves them out until a dot is typed.
    Tab takes a match; Enter takes it only when it extends what was
    typed or was chosen with the arrows, so a prompt is never sent with
    a word replaced unasked.

87. **git asks while the shell is in another repository.** Claude Code
    2.1.325 made read-only git commands ask in a repository that is not
    the project's or one you trust, since a repository's own settings
    (core.fsmonitor, filters, diff drivers) can make even git status run
    a program. gnosh already asked about git after a cd in the same line
    and about git -C and --git-dir, and about every git command in an
    untrusted folder whose settings name such programs. What was left:
    the shell's folder persists between commands, so a cd in one
    command and a bare git status in the next ran in the other
    repository. Now the engine is told each folder's git directory
    (Options.RepoOf: the nearest .git going up, a folder or the git
    directory a .git file names), and while the shell's folder is in a
    repository whose git directory is not, and does not lie inside, a
    working directory's own (a work tree or submodule of the project lies
    inside its .git), every git command asks, whatever rule allows it.
    With permissions skipped nothing asks, as for the untrusted case.

88. **Signing in to MCP servers by OAuth, as Claude Code's.** A server
    reached at a URL that answers its handshake with 401 or 403 stands
    as wanting a sign-in, and the person is told (a run says to sign in
    from a session first, as Claude Code's -p does). /mcp login <name>
    finds the authorization server (oauth.authServerMetadataUrl; else
    the protected resource metadata, RFC 9728, from the 401's
    WWW-Authenticate or /.well-known; else the server's origin, by RFC
    8414 or OpenID Connect), registers gnosh as a public client when no
    oauth.clientId is configured (RFC 7591), and gives the address to
    sign in at, with PKCE (S256), a state and the resource (RFC 8707);
    the redirect address is http://localhost:PORT/callback (any free
    port, or oauth.callbackPort), on 127.0.0.1 and ::1. When the browser
    comes back to this machine the sign-in ends by itself; over ssh the
    person pastes the address it tried to go to after /mcp login <name>.
    The issuer the answer names (iss) must be the one discovered, and
    credentials go only to https endpoints or to this machine. The token
    is kept by the server's URL in mcp-logins.json in gnosh's state
    folder, readable by its owner alone, sent with every request unless
    the entry configures an Authorization header (which is then the
    credential in force, and its refusal a plain failure), refreshed once
    when refused, and forgotten when it cannot be; offline_access is
    asked for when the server offers it. /mcp logout <name> forgets the
    sign-in without revoking it at the server; the /mcp picker offers
    both. A browser is opened only at a desktop, not over ssh, and only
    for a web address. Everything a sign-in sends goes over https or to
    this machine: a server, its metadata (every redirect of it), its
    authorization server and endpoints; a token is never sent to a
    server over plain http elsewhere; token and registration requests
    follow no redirect. The redirect address takes only the answer that
    carries the sign-in's state, and the sign-in does not start when
    another program holds its port on ::1. A refused token (401) is
    refreshed under a lock other gnosh processes take too, after the
    file is read again; only invalid_grant forgets the tokens, and a 403
    renews nothing. From a shell, gnosh mcp login <name> does the
    same (it waits for the browser, or for the address pasted on a line;
    --no-browser opens none), gnosh mcp logout <name> forgets, and gnosh
    mcp list and get use the sign-ins and name a server that wants one.
    Not built: the client ID metadata document (CIMD), and a client
    secret given when the server is added (a confidential client
    registered by hand).

89. **Prompt hooks, as Claude Code's.** A hook of "type": "prompt" is a
    rule about what to block or allow, or a condition that must hold,
    which the model judges: $ARGUMENTS in it stands for the event's input
    as JSON (without it, the input follows the prompt), and the model
    answers {"ok": true} or {"ok": false, "reason": "..."}, with
    "impossible": true when a condition can never be met. The answer is
    turned into what a command hook would have printed on the event, and
    read as that is: on Stop and SubagentStop the reason becomes the next
    instruction (unless impossible); on PreToolUse the call is refused
    and the turn ends (with continueOnBlock the reason goes to the model
    as the call's error); on PostToolUse the turn ends (or the reason goes
    to the model); on PostToolBatch, UserPromptSubmit and
    UserPromptExpansion it blocks; on PostToolUseFailure the reason goes
    beside the error; on PermissionRequest it decides nothing. Prompt
    hooks run on those nine events only, as in Claude Code. The question
    goes to the model the session asks its own side questions, at
    temperature zero, with gnosh's instructions for a judge; a hook's
    "model" is read and not honored (gnosh does not pick a model per
    hook). The timeout is 30 seconds unless the hook sets one. A model
    that does not answer in time, or not with that JSON, is a hook that
    could not run, which blocks where a hook can block, as a failed
    command does. Agent hooks ("type": "agent") came later (92).

90. **MCP elicitation, as Claude Code's.** An MCP server may ask the
    person for a few values in the middle of a call (elicitation/create:
    a message and a flat schema of string, number, integer, boolean and
    enum fields). gnosh declares the capability only in a session someone
    attends; in a run it is not declared, and a request is refused. The
    request is put as a form like the model's own questions: the server's
    message on top, under the server's name, then one question for each
    field (a boolean is Yes or No, an enum offers its values by their
    enumNames, anything else is typed in); a request with no fields asks
    whether to go on. The answers are held to each field's type, bounds,
    length and format (email, uri, date, date-time); one that does not fit
    is said and asked again, twice at most, and then the request is
    declined. Esc on the form answers cancel and the turn goes on, since
    the server decides what follows (Esc on the model's own questions
    stops the turn). Stopping the turn while the form is up takes it back
    and answers cancel; a server that takes its request back
    (notifications/cancelled), or a connection that ends, withdraws it and
    is sent nothing. A server's form goes to the person only, never to a
    channel. Not built: later MCP versions' URL mode, field defaults
    filled in, and the Elicitation and ElicitationResult hooks.

91. **The Elicitation and ElicitationResult hooks, as Claude Code's.** An
    Elicitation hook runs before an MCP server's form is shown (its
    matcher is held against the server's name; it is told the server,
    the message, "mode": "form" and the requested_schema) and may answer
    in the person's place with hookSpecificOutput "action": "accept" and
    the values in "content", or "decline" or "cancel"; one that says
    nothing leaves the form to the person. An ElicitationResult hook runs
    on the person's answer (told the action and the content) and may put
    another answer in its place; it does not run after an Elicitation
    hook answered. Exit 2, a top-level "decision": "block" and a hook that
    could not evaluate decline, and a decline wins over the other hooks'
    answers, as in Claude Code; beyond it, a cancel wins over an accept,
    two accepts with different values decline, and an answer counts only
    from a hook that exits 0. Values a hook gives are held to the fields
    as the person's are, and values that do not fit decline. A run
    declares elicitation when an Elicitation hook is set, so that the
    hook can answer there; what the hooks leave to the person is declined
    in a run. Where an ElicitationResult hook cancels, the person's
    values are not sent (Claude Code sends them with the cancel; MCP
    sends content only with accept). Command, http and mcp_tool hooks
    run on these events; prompt hooks do not, as in Claude Code.

92. **Agent hooks, as Claude Code's.** A hook of "type": "agent" gives its
    prompt ($ARGUMENTS as in a prompt hook) to a sub-agent of its own with
    Read, Glob, Grep and Bash, which the session's permission rules apply
    to, for at most 50 replies and 60 seconds unless the hook sets a
    timeout; its last message is read for {"ok": ..., "reason": ...} as a
    prompt hook's answer is. "ok": false is handled as a prompt hook's
    with continueOnBlock, Claude Code's rule: on PreToolUse the call is
    refused and the reason goes to the model, on PostToolUse the reason
    goes to the model, on Stop and SubagentStop it is the next
    instruction; "impossible" is not read. Agent hooks run on the prompt
    hooks' events except PermissionRequest, and take no continueOnBlock.
    Inside the hook's sub-agent no agent hook runs, and no SubagentStart
    or SubagentStop hook, so that a hook cannot set itself off; the other
    hooks (a PreToolUse guard) still apply to its calls. The sub-agent
    reads the instruction files, where how to run the tests often is; a
    hook's "model" is read and not honored, as for prompt hooks. A
    sub-agent that writes its 50 replies without an answer is a hook that
    could not run.

93. **An agent's MCP servers (mcpServers), as Claude Code's.** An agent
    definition may name MCP servers: an entry that is a name uses the
    session's server of that name (a name the session lacks is said, and
    the agent goes without it); an entry that is an object, a server's
    name with its settings in the shape of .mcp.json, is the agent's own.
    A sub-agent's own servers are connected when it starts and closed when
    it ends, and only it is offered their tools; the session that runs as
    an agent (--agent) has its servers from the start, with the files',
    shown in /mcp with the scope agent (an agent's own server of the same
    name as a file's wins). A project's definitions are read only in a
    folder the person trusts, and so are their servers. The definition's
    tools and disallowedTools take MCP tools by name or by server:
    mcp__github__* in tools gives every tool of github, and mcp__github,
    mcp__github__* or mcp__* in disallowedTools take them away. A server
    of the agent's own is written as its name with its settings indented
    under it; "- name: value" on one line is refused rather than read as
    a name. Not built: the memory field, which would be a second memory
    beside mem (Jae's design call).

94. **Claude Code 2.1.327's small changes, where gnosh has the thing.**
    gnosh mcp add-json <name> - reads the entry from standard input, so
    that a key in it stays off the command line (a lone - is an argument
    only of a command that reads standard input; elsewhere it is still
    refused). On a permission or trust question the cursor keys stop at
    the first and the last answer instead of going round from no to yes,
    and a number past the last answer moves the cursor to no without
    answering. A prompt's first 100 @ words are looked up, and no more.
    WebFetch refuses a URL of more than 2,000 characters before anybody
    is asked about it. Grep gives lines and counts in the order of their
    files' paths, which does not change while they are paged through with
    offset (files with a match still come newest first). /status lists
    the agent and skill files it found and does not use, and why. Already
    so in gnosh: /export never writes over a file, Up and Down keep the
    cursor's column, and Tab in a shell line completes only paths.

95. **What a review of 90 to 94 changed.** A server's form is tied to the
    calls of that server's tools that run when it asks, not to the
    session's turn: it is taken back (and cancelled) once they have all
    ended, so a background sub-agent's form outlives the main turn's end.
    No to a confirmation declines at once; a number that is NaN or
    infinite is no number; an answer JSON cannot hold is an error to the
    server, never an empty reply. In the plain session a question with no
    options takes the line as typed (a number too, and nothing for an
    optional field), and /cancel cancels a server's form. A hook's run
    stopped with the request is a quiet cancel. An ElicitationResult
    hook's accept with no values keeps the person's; two hooks that accept
    with different values decline whatever a third says. An agent hook
    whose sub-agent did not finish (its turn failed, was stopped or ended)
    could not run, and every hook's verdict is the last top-level JSON
    object with "ok" in the reply, not the first. With
    --strict-mcp-config or in safe mode the servers an agent's file writes
    are left out (those of --agents stay, as the person's word); a
    sub-agent's own server named like a session server is not started; an
    agent's own server never takes part in channels, and one that wants a
    sign-in is refused with how to give it one; the session run as an
    agent is offered only the MCP tools its definition lists.

96. **Shell mode's Tab and history, and respondToBashCommands, as Claude
    Code's.** A command run behind `!` is kept in the session's file as a
    meta entry ("shell"), so the prompt history holds it, with its `!`, in
    the folder's later sessions too (the hidden prompt that hands its
    output to the model never was history). Tab on a shell line offers,
    first, at most five commands run before that begin with the line typed
    so far, the newest first, when the cursor is at the line's end; then
    for the word at the cursor, where a command goes (the first word, a
    word after &&, ||, ;, |, & or "(", or after sudo, time, env, xargs,
    then, do and their like), the shell's own commands and the programs on
    the PATH, and anywhere else the files. An option, a quoted, computed
    or pattern word gets nothing, and the @ mention is not offered in a
    shell line. The list opens on Tab only, never while the line is typed,
    because Enter on an open list takes the candidate and a shell line
    must run as typed. The respondToBashCommands option (default true; in
    /config, live) with false puts the command and its output in front of
    the model with the next prompt, as a stopped command already was,
    instead of starting a turn. This replaces the last words of 94 (Tab
    in a shell line completed only paths).

97. **Servers written at the top of a file of servers.** An entry beside
    "mcpServers" instead of inside it that is a server itself (an object
    with a "command" or a "url") was never read, without a word. Now
    reading the file names such entries ("it holds the server "github"
    at its top level, outside "mcpServers", where no server is read: move
    it inside"), and gnosh mcp add into that file stops and leaves it as
    it is, as Claude Code 2.1.327's mcp add --scope project stops instead
    of writing over such a file. Other keys at the top are kept, as
    before.

98. **A command named partway through a prompt.** As Claude Code's does,
    a word that begins with a slash after a space, in a prompt (not on a
    line that is itself a command, whose words are the command's), is
    completed from the commands and skills whose names begin with what
    follows the slash, then from the commands' other names; a bare slash
    lists them all. When no name begins that way, or the word goes on
    past a second slash, it is completed as a path, as before, so /tmp
    and /srv/x stay paths. The command does not run from there: the model
    reads its name. The slash already opened the list as it is typed;
    Enter takes a candidate that completes the word, as it does for @ and
    paths.

99. **/simplify, and /review's --fix and levels, as Claude Code's.**
    /simplify [what to look at] sends the model the same range /review
    covers with gnosh's own task: make the changes simpler, the code
    doing what it did, looking for reuse of what the repository has,
    simpler code, less work and the right place for each change, then
    run the tests; bugs are /review's. Claude Code's /simplify runs four
    review agents at once; gnosh asks the one model once, since its boxes
    hold one model on one GPU. /review takes Claude Code's /code-review
    name as another, its --fix (the bugs the model is sure of are fixed,
    then the tests run; the rest are left and named) and its first word
    low, medium, high, xhigh or max (low: at most three findings it is
    most sure of; high and up: every changed function and its callers,
    the less sure findings too, marked). PR numbers, --comment and ultra
    are not taken: they need a forge or Anthropic's cloud. The notice
    names what the model is doing.

100. **What a review of 96 to 99 changed.** A shell line never opens the
    list by itself (the editor's "/" and "@" triggers opened it there),
    and Enter on an open list runs the line as typed unless a candidate
    was chosen with the arrows: a command run before begins with what was
    typed, so Enter took it, and a second Enter would have run an older
    line. A slash opens the list by itself only where it begins the box
    and makes the line a command; in the middle of a prompt Tab opens it,
    so a route such as /me or /st is sent as typed (this replaces the end
    of 98). The PATH's programs are listed in the background, once and
    again after five minutes, never on a key: a folder on the PATH that is
    slow or never answers held up the interface, a fifo there held its
    open for ever (an entry that is not a directory is passed over now),
    and a folder past 4,000 names lost programs (now 50,000). A command's
    output that the model does not answer now (stopped, or
    respondToBashCommands false) is written to the session's file at
    once (the agent's Record: between turns, before the next prompt), not
    told with the next request after the prompt, where it came after the
    prompt and was lost if the session ended first. /mcp with words after
    it in a session with no server of its own says so instead of failing
    over an empty list, and a sub-agent's own server that wants a sign-in
    is not offered /mcp login. A prompt's leading blanks stay in the
    history, so " !x" and " /x" come back as prompts.

101. **Skills stacked in one line, as Claude Code's prompt has them.**
    "/write-tests /fix-issue 123" typed in a session loads both skills,
    in that order, in one prompt to the model, each with "123" as its
    arguments. Up to five more after the first (six in all); the first
    word that is not a skill the person may run in this conversation (a
    command, a word, a skill switched off, one for the model alone, or
    one that runs in a sub-agent of its own) ends the stack, and it and
    what follows are the arguments. A run (gnosh -p), as Claude Code's
    -p, loads the first skill alone. Each skill's permission rules,
    effort and hooks come in as when it is typed alone, and the
    UserPromptExpansion hooks are asked about each.

102. **askUserQuestionTimeout, as Claude Code's.** The option (60s, 5m
    or 10m; the person's own files and /config only, live) gives the
    model's questions (AskUserQuestion) a time without a key after which
    the full interface closes them by itself: what was picked by then
    goes as the answer, with the Choice "away", and the model reads that
    nobody answered in time, that the person may be away, what had been
    picked, and to go on with its own judgment and ask again later. Any
    key restarts the time; the last twenty seconds count down on the
    line above the question. Permission questions, plan approval and a
    server's form (an MCP elicitation) never close so. Claude Code also
    stops the time while the terminal says its window has focus; gnosh
    does not read focus (tmux drops it unless told), and counts from the
    last key alone. The plain session asks line by line and has no
    timer.

103. **A command of several lines, as the permission question shows it.**
    The prompt mark ($) stands on the command's first line only, and the
    lines after it are set under the command: a here-document's body and
    a continued line are part of one command, and a mark on each made
    them read as commands of their own. Tabs stay tabs, which the
    interface sets out as spaces (they were escaped as \x09, which made
    Go code in a here-document hard to read); every other control
    character is still escaped. Found in a box check, where Qwen asked to
    run a Go program it wrote in a here-document.

104. **What a review of 99 to 103 changed.** A reopened session counts as
    having sent a prompt only when its file holds a reply, a prompt, a
    turn's end or a summary: a command's output written between turns
    (Record) is not one, and the first prompt still carries the
    instruction files and what else a first prompt carries. A note Record
    keeps while a turn runs and no request of that turn takes is written
    as the turn ends, and when the session ends. Any key, paste or mouse
    event, the transcript view's keys and the return from Ctrl+Z count as
    the person being there, for a question's time without them. The
    skills of a typed line are first all made and their commands all
    checked against the rules, each under its own allowed-tools alone,
    and none run; a skill that cannot be run stops the line with nothing
    of any run or left in force; then the commands run, each skill's
    under its own grant, and only then do the line's grants, thinking
    levels, hooks and records come in. The permission question puts the
    prompt mark on every line where the shell starts a command (quotes,
    here-strings and shifts inside $(( )) are followed, so a << there
    opens no document), and sets under it only a here-document's text, a
    line that goes on, and what stands inside quotes across lines (this
    corrects 103, which set every line after the first under it).

105. **gnosh --bg, gnosh logs <session> and gnosh stop, as Claude Code's
    claude --bg, logs and stop.** --bg (also --background) starts a new
    session in the background on the prompt and returns at once: the same
    kind of run /background and /fork start (gnosh -p in a process of its
    own, the command line written out by RelaunchArgs with a new session's
    id, --bg left out), its output in <session>.background.log beside the
    session's file, and now its process id in <session>.background.pid
    (/background and /fork keep it too). It is refused with -p, -c, -r,
    --no-session-persistence, or without a prompt. gnosh logs takes a
    session's id or the start of one and prints that run's log; --follow
    follows it until the run lets go of its session. gnosh stop (also
    kill) asks the run to stop (SIGTERM, which ends it as Esc ends a
    turn), and ends it after ten seconds if it still holds the session;
    it acts only on a process whose command line names the session, so a
    process id used again by another program is never reached. Claude
    Code's agent view, attach, respawn and daemon are not built: gnosh's
    runs ask nobody anything, and gnosh -r opens one once it is done.

106. **A background run is named by the first 13 characters of its id.**
    gnosh's session ids begin with the time, so two runs started within
    a minute share the first eight characters, and gnosh stop with them
    found two runs (found on box 1, where a check started two runs 30
    seconds apart). gnosh --bg's hints give the start of the id up to
    the end of its second group, the time to the millisecond, and lay
    the three hints out in one column; gnosh logs and gnosh stop take
    any start of an id that one run alone has, and name every match when
    more have it.

107. **/tasks stop reaches the folder's background runs.** /tasks names
    each session in the background by the first 13 characters of its id
    (106) and, while it runs, says /tasks stop <name> stops it; /tasks
    stop takes a sub-agent's id, a background command's, or the start of
    a run's id, which it stops as gnosh stop does (one stopRun for both).

108. **gnosh agents lists the sessions in the background of every folder.**
    As Claude Code's claude agents does with no --cwd, and as its agent
    view groups them, gnosh agents prints the runs gnosh --bg,
    /background and /fork started from any folder: the working ones
    first, then those done in the last week (--all: every one whose log
    is kept), each by the 13 characters of its id (106) with when it
    started, how it stands (working, done, or stopped), its folder and
    its prompt. --cwd keeps the runs of that folder and the folders in
    it. --json prints the working ones as Claude Code's --json does, and
    with --all the rest, each with id, kind "background", cwd, startedAt
    and state, the process id while it works, and gnosh's own prompt,
    log and status (the last line the run printed). Claude Code opens a
    view to work in here; gnosh prints the list, since its runs ask
    nobody anything. A run's log now names its folder (logs from before
    give it by the session's first entry), and gnosh stop adds a line
    saying the run was stopped, from which the list tells a stopped run
    from one that is done. gnosh logs and gnosh stop now find a run of
    any folder, as the names gnosh agents prints are of any folder;
    /tasks stop still reaches only this folder's. gnosh logs prints the
    last 1000 lines, as its help and the spec say (it printed 100).

109. **gnosh prompt show, path and export (CLI-12).** Jae asked how to
    read and change what each prompt sends; internal/prompt already held
    every text as a file, with Load (a session reads the person's
    replacements from the config's prompts folder at its start) and
    Export, but nothing on the command line reached them. gnosh prompt
    show lists the texts, where each goes (the system prompt, a line on
    the person's side, inside a tool's result, a small request of
    gnosh's own), about how many tokens it is, and which are the
    person's; with a name it prints the text in force on standard
    output, and what it is, when it is sent and what it must keep on
    standard error, so the text alone can be kept in a file. A folder
    whose files cannot stand in is refused with Load's words, as a
    session refuses it. gnosh prompt path prints the folder; gnosh
    prompt export writes gnosh's texts there (all of them, or those
    named; each name checked before anything is written), keeping a
    file that is there unless --force. The spec's tools and context
    subcommands are not built: /context all says the same in a session
    or in gnosh -p. "prompt" is now a command word, so a one-word prompt
    "prompt" goes after -- (or -p), and a word after gnosh prompt that is
    none of its commands says so; the parser now says, for any command
    first on the line, that words meant as a prompt go after --.

110. **A run in the background works while its process does (a third
    review of 105 to 109).** A run was taken to work while its session's
    file was held, which is wrong at both ends: a run started with --bg
    holds nothing until it writes its first entry, so gnosh stop said
    "not running" and the run went on, and gnosh logs -f stopped before
    the run's last lines; and a session opened in a gnosh of its own read
    as a run at work. A run now works while the process kept beside its
    log is gnosh at work on its session (runAlive); the hold on the file
    tells only for a run without a kept process. gnosh stop waits up to a
    minute, not ten seconds, before it ends a run, since a stopped turn
    waits at the end of a session for a mem call under way (30 seconds),
    sub-agents in the background (15) and hooks (10): ended sooner, a
    push or a pop could be lost. /tasks stop reaches a run of any folder,
    and waits beside the session. gnosh --bg leaves --worktree to the run
    (the process that started it made a tree of its own, which leaked),
    takes what is piped in with the prompt, as gnosh -p does (up to 100
    KiB, which goes on the run's command line), and takes the flags of
    print mode at a terminal. A run's log is read for gnosh's own lines
    only where gnosh writes them, so a run that quotes them is not taken
    at its word, and a session sent off from two folders is told by its
    working run, or else its newer one. /fork's log names the copy's own
    file, which is under the folder the session began in. The shell
    reader of 103 follows quotes inside $(...) and backquotes inside
    quotes, $'...' strings, and here-document words with any character
    the shell takes in a word (END-OF-SCRIPT), and the ssh hint reads a
    line together with the lines a backslash carries it into.


111. **The gnokey tool (TOOL-40, TOOL-41; D15).** Jae asked for "a real
    Gno integration (gno toolchain, realm deploy, gnokey signing)"; the
    earlier gnosh had the tool, and the rewrite had built the masked
    prompt for it (a question of the kind "secret" in both front ends)
    but not the tool. The tool, gnokey, takes the command line as typed
    and runs gnokey with no shell (shellquote.Split refuses what only a
    shell carries out). It asks permission as the shell does, showing the
    command. A command that signs, gnokey sign, or maketx unless
    -broadcast=false (gnokey of November 2026 signs and broadcasts by
    default; with -broadcast=false it prints the transaction unsigned,
    checked on the devbox), asks the person for the key's password at the
    masked prompt, naming the key (the command's last word) and the
    command, and gno.land's main network when the chain ID (gnoland1) or
    the node (rpc.gno.land) is its. The password goes to gnokey on its
    standard input by -insecure-password-stdin, which gnosh puts before
    the key, and is taken out of the output; the model passing that flag
    is refused, as are add, delete, rotate, import, export, generate and
    recover. Esc at the prompt stops the turn. Only a session somebody
    attends has the tool, in a project that holds Gno code (as the
    system prompt's part about Gno is sent), where gnokey is on the PATH:
    a run, a sub-agent, another project and a machine without gnokey
    have none, and pay nothing for it in the prompt. The permission
    engine knows the name from the program (Options.KnownTools), so an
    agent's tool list can leave it out. The shell refuses -insecure-password-stdin in
    any spelling, quoted or behind a pipe, for the model and the person
    alike, so that only the masked prompt signs; told so, a model is to
    give the person the command and stop. Tested with a stand-in gnokey
    and, where the machine has it, the real one (a key made offline, a
    wrong password reported, the right one signing).

112. **A session /cd moved is given the new folder's first-prompt block.**
    The box check of 18 November found that after /cd the model did not
    know the moved-to folder's GNOSH.md: instruction files, kinds of
    sub-agent, skills and the environment go with a session's first
    prompt only, and since the fix of 104 a session opened again (as
    the gnosh /cd starts is) has had its first prompt. On 17 November it
    had worked only because a reopened session still counted as not yet
    sent. The note that tells the model of the move is now followed by
    that block for the folder the session came to.

113. **The gnokey tool, after its security review.** A reviewer read 111
    against gnokey's own source and found eleven defects, three high,
    all closed here but the shell's text match (below).
    - A password typed at once after the approval went to the prompt
      box, where Enter sent it to the model: the TUI's guard routes keys
      typed before a question could be read to the box. A masked
      question now takes keys the moment it is drawn, and what was typed
      into the box since the question before it closed (within ten
      seconds) is taken out again. The plain front end, which reads
      lines typed ahead as prompts and holds one question at a time, is
      given no gnokey tool.
    - The refusal of key management read the first word anywhere that
      named a command, as typed; gnokey takes the first word after its
      flags, in any letter case ("gnokey Delete -force k", "gnokey
      -remote list delete k" deleted keys). The tool now reads the line
      as gnokey does: the command first, any case, from a list of those
      it runs (query, list, sign, verify, broadcast, maketx, version;
      maketx's call, run, send, addpkg, session), flags told by gnokey's
      own boolean flags, the key the first word that is no flag (the one
      before the file for maketx run, which the last-word rule got
      wrong), -master named in the prompt.
    - The password went to whatever file was at gnokey's path when a call
      ran, which the model could rebuild or replace. The file's identity
      (device, inode, size, time) is kept when the session starts, and a
      call refused when it changed; gnokey runs without the terminal.
    - -config (flags from a file the person is not shown) is refused; a
      word naming a device, pipe or socket (/dev/stdin under any name,
      which turned the password into data) is refused; a word with a line
      break is refused; the password is scrubbed as typed and as JSON
      writes it; the prompt says the main network first and names the
      folder when it is not the session's; a stop after the password says
      the transaction may have gone out.
    - Left: the shell's refusal of the password flag is a text match,
      which a -config file or a pseudo-terminal can get around. It needs
      the password, which the model is never given; the permission rules
      ask about such lines in default mode.

114. **A self-paced /loop's run ends when it schedules the next.** The box
    1 sweep of 18 November found Qwen, after calling ScheduleWakeup,
    checking the clock and calling it again every few seconds: each call
    moved the next run a minute on, the turn never ended, and the run
    never came. ScheduleWakeup now ends the turn with its round (the
    tool result's halt, as EnterWorktree's), its result saying so, and a
    second call in the same round is refused without moving the run.
    stop true does not end the turn: the model may say what came of the
    loop.

115. **gnosh agents on a terminal is Claude Code's agent view.** Claude
    Code's `claude agents` opens a view to work in
    (code.claude.com/docs/en/agent-view, read 18 November 2026); gnosh's
    printed a list (108). Now, on a terminal and without --json, it takes
    the whole screen: the runs grouped as Working and Completed, each
    with its name, what it does now (the call it makes, read from the
    end of its session's file, or "thinking" while the model has the
    turn) or the last line it printed, and its age, frozen at its end for
    one that is over; a box at the bottom starts a run on a prompt (gnosh
    --bg run as a child, in the view's folder; under four characters is
    "Too short", as Claude Code's), and n:, s: and o: in it filter the
    list. Space peeks (the prompt, the last steps or the answer), Enter
    watches a working run's conversation as the transcript view lays it
    out, growing as the file does, or opens one that is over with gnosh
    -r, the view giving the terminal up and taking it back after; Ctrl+X
    stops a working run, and twice within two seconds takes one that is
    over off the list (its log goes, its session stays). Off a terminal,
    and with --json, the list is printed as before. Left out, because
    gnosh's runs are -p runs and not live sessions: attaching to a working
    run's prompt (it is watched instead), replies sent from a peek, the
    Needs input group (a run asks nobody), pins and the idle-process
    reaping they guard against, PR labels, grouping by folder, and the
    `<` key that backgrounds a session into the view (/background does
    that).

116. **The working mark is a fall, and says how deep a thought is (UI-50;
    Jae, 18 November 2026).** He asked for "something like the yellow ->
    deep in thought thing" of Claude Code's working line, "except keep
    it multi colored sparkly ... start with more boring bland even
    colors first ... movement like dropping rightward, as if
    accelerating to the right, like gravity goes sideways, and when
    'deep in thought', more things drop faster with higher
    acceleration" (AUTHORSHIP.md has it whole). The comet of 14 November
    gives way to it (`internal/ui/fall.go`).
    - The mark is a strip of ten cells. A drop (the comet's marks: a
      head and up to three tail marks) starts at the left edge nearly
      at rest and is pulled to the right at a steady rate, so that it
      crosses each cell sooner than the one before; its tail is one
      cell, and one more for every seventh of a second's travel. The
      strip is never empty.
    - How it falls follows the one thing the session knows of a thought:
      how long the reasoning that is arriving has gone on without a
      break. Up to five seconds, and whenever the model is not thinking
      (waiting, writing, running tools), the fall is calm: one drop at a
      time, a pull of 5 cells a second each second (a crossing takes
      1.8 s), and every mark only 15% of the way from the palette's grey
      to its own colour, none bold. From five seconds to forty it
      deepens evenly, the colour ahead of the rest (half way to deep the
      marks have four fifths of their colour); at forty it is deep:
      three drops at a time, a pull of 30 (a crossing takes 0.75 s),
      every mark in its own colour (the blue-green, the purple, the hot
      pink and the gold that every fade of gnosh's runs through, a
      drop's head in one and its tail in the next ones), the heads bold,
      and about three heads in ten flashing toward white at any moment.
      More than three drops at once was tried and filled the strip, so
      that nothing in it was seen to move. A frame is 110 ms while calm
      and 60 ms when deep. When the thought ends the fall is calm again
      within a second and a half.
    - The line's words change with it, since nothing is said by a mark
      alone (UI-52 keeps them one colour): "is thinking", and from forty
      seconds "is deep in thought". Those three words are his, from the
      prompt; none of the other program's other labels, its colours or
      its timings are used, and the figures above are gnosh's own
      (spec/cc/interactive.md, "Deliberately not adopted").
    - With 256 colours each colour is the nearest the terminal has, so
      the calm fall is in plain greys and the colours come in a few
      steps; with sixteen a bland mark is faint in the terminal's own
      colour until about thirteen seconds into a thought, and from there
      in the terminal's colours; with no colour, and while a question
      waits, the plain mark stands still, as before.
    - It is gnosh's own drawing: marks moved by a rule of motion, and
      not the earlier program's animation. Nothing in it is soft
      (UI-51): a tail is whole marks, each in one plain colour.
    - The session that wrote it had, on 12 November, read how the other
      program makes its line, to answer a question of his about that
      program. PROVENANCE.md's rule for a builder who has seen another
      implementation was followed: the fall was designed from his words
      above and from how a thing falls, and shares with that line the
      idea he asked for and nothing of how it is made.

117. **A run tells the model what its end stops, once.** gnosh stops, as
    a session ends, what its commands left running (946cfaa, after the
    servers a test harness's runs had left on box 1 for two weeks), and
    the command's result says so and how to keep one (setsid). On the
    FrontierHarness run of 18 November Qwen started a server with nohup
    ... &, said done, and the run's end stopped the server before it was
    graded; Pi, which leaves such processes running as Claude Code does,
    passed with the same command. Rather than leave everything running
    again, a run (-p, also --bg) that is about to end with such processes
    still there tells the model once which they are, and gives it a turn
    to start one again with setsid if the task needs it. A run that left
    nothing running is not told, and that turn's failing does not fail
    the run.

118. **A run in the background waits on its permission questions for the
    agent view.** Claude Code's background sessions stop under Needs input
    and are answered from the agent view; gnosh's runs refused every call
    that asks, so a run started from the view without -y could do little.
    Now gnosh --bg, /background and /fork hand the run, in its
    environment (GNOSH_RUN_ASKS, taken out before anything is started),
    the base of its log's name; the agent loop puts a permission question
    to the run's front end even with nobody attending (agent.Options.Relay,
    permission questions only: a key's password, the model's own questions
    and an MCP server's still have nobody to ask), and the run writes it to
    <base>.ask with a random token and waits, with no clock, for
    <base>.answer. The view lists such a run under Needs input (state
    "blocked" in gnosh agents --json, as Claude Code's), shows the
    question with its answers numbered, and writes the answer with the
    token; a number answers, and words with Enter answer no with them,
    which the model reads. An answer without the token is not heard, so a
    model that wrote the file itself would not be either, and the token is
    in no file the model was shown. gnosh stop still ends such a run.

119. **gnosh --bg goes on with a session -c or -r names, and the agent
    view's peek replies with it.** Claude Code's claude --resume <id> --bg
    continues that session in the background, and a reply to a finished
    session from its peek restarts it on the reply. gnosh --bg took a new
    session only; now -c (the folder's newest session no gnosh holds
    open) and -r with an ID, the start of one, a file or a name send that
    session back to work: the run resumes it (--resume), its log is beside
    the session's file, and a session open in another gnosh is not sent.
    In the agent view, words typed while peeking at a run that is over,
    and Enter, run gnosh --bg -r on it in its folder (without the peek
    they start a new run, as Claude Code's do). A working run takes no
    words yet: that needs a way into a run's queue, which is left for
    later. --fork-session does not go with --bg.

120. **Words sent to a working run reach it between its steps.** Claude
    Code's peek replies to a working session join its message queue.
    A run in the background (one that was given GNOSH_RUN_ASKS, 118) now
    writes a random token to <base>.token once its prompt is in, and reads
    <base>.inbox-<time>.json files every second while it works: each
    message that carries the token is submitted as the person's prompt,
    which the model gets between two rounds, and the run stays open for a
    turn such a prompt starts; one without the token is thrown away
    unheard. The agent view writes them (words typed while peeking at a
    working run, and Enter). The run stops reading once it has nothing
    left to do, and what it did not read stays in its inbox: the view,
    seeing the run over, takes the words back and sends the run back to
    work on them (119), so no reply is lost to a run that ended first.

121. **The agent view tells the person when a run needs input or is
    done.** Claude Code's view sends a notification through
    preferredNotifChannel when a session needs input, finishes or fails,
    and its tab title counts the sessions awaiting input. gnosh's view now
    does both: a run that comes to need input, or comes to be done, since
    the last look is said by the person's channel (the same notes the
    session sends a person who seems away: a desktop note where the
    terminal takes one, the bell with terminal_bell, nothing in tmux with
    auto), and the title reads "N awaiting input · gnosh agents" while
    any wait, the terminal's own title coming back as the view closes.
    What was there as the view opened is not told. A run stopped by the
    person is not told either: they know.

122. **The agent view's box takes /model, /effort, /exit and @agent.** In
    Claude Code's view, /model, /effort and /fast set the defaults of the
    sessions dispatched from that window, not saved, with default
    resetting them; @<agent-name> or "<agent-name> <prompt>" runs that
    custom agent as the session's main agent; /exit or /quit closes the
    view. gnosh's box now does the same for /model and /effort (gnosh has
    no /fast): the runs started or sent back to work from the view are
    given them, default puts back what gnosh agents was opened with, and
    a bare /model or /effort says what new runs take. "@name prompt"
    starts the run with --agent=name. "<agent-name> <prompt>" without
    the @ is not read so: a prompt may well begin with a kind's name.

123. **A session's status line counts the runs that wait on the
    person.** Claude Code's footer counts the background agents waiting
    on you ("← 2 agents"). An attended gnosh session now looks at once and
    every five seconds at the runs in the background of every folder, and
    while any waits on a question its status line says "1 run needs input:
    gnosh agents", where it is answered.

124. **Ctrl+S groups the agent view by folder.** Claude Code's Ctrl+S
    switches the view between grouping by state and by directory; grouped
    by directory each summary starts with its state, and a prompt is
    dispatched into the selected row's directory. gnosh's does the same:
    the folders by name, the runs that wait on the person first in each,
    each row's status led by "needs input", "working" or "done" (a stopped
    run's says so already), and a prompt starts its run in the folder of
    the run chosen, which the keys line names. Unlike Claude Code's, the
    choice is not remembered from one opening of the view to the next.

125. **The agent view calls a run by its name, or its prompt, and Ctrl+R
    names it.** Claude Code's view names every session (a small model
    writes the name) and Ctrl+R renames one. gnosh's list now calls a run
    by the name it was given, or else by its prompt's first line, as a
    session is named after its first prompt (no model call: the boxes have
    one GPU); notes and the peek's head keep the short id, which gnosh
    logs, stop and -r take. Ctrl+R puts "/rename <its name>" in the box,
    and Enter names the run chosen so (an empty name takes it away): a
    line of the run's log of its own, "gnosh: <time> its name is now:
    <name>", which readRunLog reads wherever it stands, and gnosh --bg -r
    keeps the name for the run it starts.

126. **gnosh attach opens the agent view on one run.** Claude Code's
    claude attach <id> attaches to a background session, and leaving it
    returns to the agent view. gnosh attach <session>, a run named by its
    id, the start of one, or its name, opens the view on that run, every
    folder's runs listed however old: a working run is watched as it goes,
    one that waits on a question shows the question, and one that is over
    opens as gnosh -r opens it, the view coming back when that session is
    left. With no terminal it says that gnosh logs -f follows the run.
    gnosh --bg's hints name it.

127. **After a review of the agent view's day of work.** A reviewer read
    the view, the question and inbox files and --bg's resuming afresh,
    and found ten things; these were fixed. Two questions a run puts at
    once (a sub-agent's beside the main agent's) shared one .ask and one
    .answer, so one could be lost and its run wait unseen: each question
    now has its own pair, <base>.ask-<id>.json and .answer-<id>.json, and
    the list shows the one put first. Before a run starts (gnosh --bg,
    /background, /fork) what an earlier run of its session left of
    questions, answers and words is cleared, and its token is written
    then, so that words sent in its first moment carry it. With a question
    shown, a command or a filter typed in the box keeps its meaning
    instead of answering no; a long question keeps its head and its answers
    in sight, and Enter again watches the conversation, where it is whole.
    A run the person stopped is not told of as done and is not sent words
    again; the inbox reader is waited for as a run ends, and a prompt it
    handed over as it stopped is still waited for; renaming a run that is
    over keeps the time it ended, and the rename line names the session.
    The token is honest about what it guards: it stops a model that writes
    the answer or inbox file blind; code the model runs without being asked
    (-y, broad allow rules) could read it, as it could do anything else.

128. **A run that failed says so.** Claude Code's list tells a session
    that ended with an error (red, Failed) from one that is done, and one
    whose process was ended from outside (Stopped). gnosh's runs had no
    word of how they ended, so every run that was over and not stopped
    by gnosh stop was done, a crash and a reboot too. Now a run in the
    background ends its log with one line of how it ended: "the run is
    done.", "the run failed: it ended with exit status N." or "the run was
    stopped by a signal (exit status N)." (runEnded, written by the run
    itself as Main returns, after a recovered panic too). gnosh agents
    (--json state "failed", as Claude Code's), /tasks, the agent view and
    /fork's notice read it: a failed run is marked ✗, its row says failed
    and what it printed last, its peek how it ended, and the view tells
    of it as of one that is done; s:failed lists them. A run whose
    process is gone, that has a process id beside its log and ended its
    log with no such line, failed without a word: it was killed, or its
    machine stopped. One with no process id yet is starting (or an older
    gnosh started it), and stays done when it is over, as before; runs an
    older gnosh started that are over with an id read as failed until
    their logs age out of the week. The peek's question lines now wrap
    instead of being cut at the screen's edge (a box check showed "Why
    gnosh asks" cut at 120 columns).

129. **← twice in a session's empty box opens the agent view over it.**
    Claude Code's ← on an empty prompt backgrounds the session and opens
    its agent view on the session's row; the first press says "Press ←
    again". gnosh takes the same two presses (within two seconds; another
    key between them starts over) and opens the view over the session
    instead of sending it away: gnosh's background is a run of its own
    (/background), and the session that opened the view is the one the
    person comes back to with Esc. The session goes on underneath as it
    does under the transcript view (Ctrl+O): what it says meanwhile is
    held and goes into the scrollback when the view closes, and a
    question of its own closes the view so that it is seen. In the view
    everything works as in gnosh agents (the runs of every folder, peek,
    answers, words, Ctrl+X, dispatch), except Enter on a run that is
    over: that opens its session in place of this one (/resume <id>,
    which keeps this one and refuses while a turn runs), not a gnosh -r
    inside gnosh. The view is the same model (agentView) drawn by the
    session's loop (agentsover.go; agentScreen is shared with gnosh
    agents' own loop); its notes go by the person's channel, and the
    terminal's title is left to the session. The status line's count of
    runs that wait on the person reads "← 1 run needs input", as Claude
    Code's footer reads "← 2 agents".

130. **The agent view's day, polished after its box checks.** Box 1
    showed a killed run's age as 0s (its end was taken from its log's
    time, which a killed run never writes again): a run's end is now the
    time on its end line or gnosh stop's, and for one that ended without
    a word the last its log or its session's file was written. The peek
    of a run that printed nothing showed its grey's colour codes as
    text: gnosh's own words there are no longer made safe as a run's
    words are. The view's box takes /stop, Claude Code's reply that stops
    a run at once (here the run chosen, as Ctrl+X does), and s:needs
    lists those under Needs input. gnosh agents --json gives Claude
    Code's waitingFor ("permission prompt") for a blocked run, and
    sessionId. And as Claude Code's footer reads "← 2 done" when sessions
    finished and none waits, a session's status line counts the runs that
    ended since the person last looked at them from the session (its
    start, or the agent view opened over it, whose listing counts as
    looking): "← 2 runs done, 1 failed"; runs that wait on the person
    still come first, and stopped ones are not counted.

131. **The agent view's group keys, and a session's first seconds.**
    Claude Code's Alt+↑ and Alt+↓ move between the view's group headers:
    gnosh's headers are not rows to choose, so Alt+↓ chooses the first run
    of the next group, and Alt+↑ the first of the group chosen, or of the
    one before when it is that one already. Ctrl+F is Claude Code's find
    by name, the n: filter: it puts n: in the box. And as Claude Code's
    footer reads "← for agents" for ten seconds after a session opens, a
    session that can open the agent view says so on its status line for
    its first ten seconds, when no run waits on the person.

132. **@<repo> in the agent view's box.** Claude Code's dispatch box
    takes @<agent-name> and @<repo>: the second runs the session in that
    directory, from the git repositories one level below, the worktrees
    in the repository and the directories that have sessions, and an
    agent of the same name wins. gnosh's "@name prompt" now starts the run
    as that kind of agent when one is named so (the view is given the
    kinds there are), or else in the git repository one level below the
    view's folder that is named so, or else in the one folder runs work
    in whose last part is the name (two such folders name none); a name
    that is none of these is taken for a kind, as before, so that the run
    says which kind it does not know. Only the first word counts, as
    before, and nothing is listed while typing; worktrees are not looked
    for. The note that a run started now names its folder.

133. **Words typed while watching a run reach it.** Claude Code's attach
    is the session itself: what is typed goes to it. gnosh's agent view
    watched a working run's conversation read-only, and words for it were
    typed in the peek, in the list. Now the watch screen has the box under
    the conversation ("words for this run: it reads them between its
    steps"): Enter sends what is typed to a working run between its steps,
    or sends one that is over back to work on it, as the peek's reply
    does; Esc first empties the box, then goes back; ← on an empty box
    goes back too. q no longer goes back from a run watched, since it is a
    letter in the box (Esc, ← and Ctrl+C still do).

134. **A run watched that asks shows its question.** A permission
    question is not in a run's conversation (it waits in its .ask file),
    so watching a run that came to one showed only "blocked", and the
    person went back to the list to peek and answer. Claude Code's
    attached session shows its dialog where the session is. The watch
    screen now shows the question above the box (its title, up to four
    rows of its text, its numbered answers): a number answers it, and
    words with Enter answer no, with them, as in the peek (answerWords,
    shared). The peek still shows the whole text.

135. **A run stops what its commands left running before it tells the
    model.** DECISIONS 117's note told the model, as a run was about to
    end, that what its commands left running would be stopped, and to
    start what the task needs again with setsid. But the old copy still
    ran: a server started again beside it could not take its port, the
    check the model made reached the old one, and the run's end stopped
    that one, so no server was left. (The FrontierHarness peer's second
    lost task, pypi-server, on a build before 117: `nohup python3 ... &`,
    started again the same way after an edit, checked on its port, done,
    and stopped at exit; Pi kept it and passed.) Now the run stops them
    first (shelltool Manager.StopLeftRunning, the sweep Close makes) and
    tells the model they were stopped; one it starts again with setsid
    finds the port free and outlives the run.

136. **Runs started from the agent view over a session keep its mode.**
    Claude Code's sessions dispatched from an agent view opened with ←
    start in the directory's defaultMode, or else in the mode of the
    session the view was opened from. gnosh's view over a session (←
    twice) started its runs with no mode, so from a session started with
    -y every run stopped at its first command to ask. Now the view is
    given the session's mode for the runs it starts
    (--permission-mode=<mode>); a session in the default mode names none,
    and the run reads its folder's defaultMode itself. gnosh agents keeps
    its own --permission-mode flag for the runs it starts.

137. **After a review of the agent view's evening.** A second reviewer
    read 963b235..d1eae3e and found five things; these were fixed. The
    serious one, in the permission engine, is its own commit: a git -C
    with a .. (git goes through a link before its ..) or a pattern is no
    longer taken for the line's own folder, and the work-tree guard no
    longer folds a .. in a git place. In the agent view: a question that
    has just come takes no answer for a moment, as the session's own
    questions do (guardShown since it was first listed, guardPause since
    the key before); a digit typed in that moment is typing, and words
    begun before the question was seen go to the run, which reads them
    once it goes on, instead of answering it "no, with them". The
    session's mode is given only to runs started in the session's own
    folder (another folder's defaultMode stands, as Claude Code's comes
    first). A run's end line is written to the millisecond, so that the
    status line's count of runs done since a look in the same second
    finds it; an end line followed by more of what the run printed is a
    quote, not its end; and how a run ended is shown as one line.

138. **The fall runs through the words, behind one mark (Jae, 18 November
    2026; replaces the strip of 116).** Of the ten-cell strip of 116 he
    said: "make it two part (it already is) where the first part is like
    claude, i think a single letter, and then the second part is the
    text that starts non-white and the whole text is animated. the thing
    that moves rightward is a different color of the text, but the text
    is always the same... and the transition to fasater/more rightward
    should be faster".
    - The mark is one cell again: a spark that turns through four of
      gnosh's own sparkle marks, swelling and dwindling, six shapes a
      second while calm and twelve in a deep thought, in the colour of
      the drop that started last. One changing character before the
      words is what he asked for by "like claude"; the shapes are
      gnosh's (UI-50's sparkle) and not that program's.
    - While gnosh works the words of the line are no longer the
      terminal's own colour: they stand in a grey between the quiet grey
      and white (the quiet grey itself on a light background), and what
      follows them (the time, the key that stops) in the quiet grey, as
      before.
    - The drops of 116 fall through the letters themselves, as colour. A
      drop starts at the first letter and is pulled to the right along
      the whole line, the time and the keys included. The letter its
      head is over takes the drop's colour, and so do the letters behind
      it: one, and as many more as the drop crosses in a seventh of a
      second, up to eight, the first four in the head's colour and the
      rest in the next, fading into the letters' own. No drop goes
      faster than 60 cells a second, however long the line. No letter
      changes, moves or is hidden. (UI-52's one colour holds for every
      letter no drop is over; the colour that passes is what he asked
      for.)
    - Depth is as in 116 but comes sooner: it begins three seconds into
      one unbroken thought and is full at fifteen, where 116 had five
      and forty, and "is deep in thought" is said from fifteen. Calm:
      one drop at a time, a pull of 8 cells a second each second, pale
      (a tenth of the way from the colour of the person's own words to
      its own colour). Deep: three at a time, a pull of 40, the four
      colours of 116, bold heads, glints toward the pale colour, the
      mark bold. Frames, settling, and what happens with no colour and
      while a question waits are as in 116.
    - With sixteen colours the words are faint and a calm drop is one of
      the terminal's whites; on a light background a calm drop is the
      terminal's own colour over grey words.
    - "The transition ... should be faster" was read as the build-up to
      the deep state, not as a stronger pull on each drop. The pull was
      raised a little all the same (5 to 8, 30 to 40), since the line is
      several times longer than the strip was.

139. **The credits and the legal notices are in the interface.** gnosh's
    licence (LICENSE.md, the Additional Terms) asks an interface built on
    gnosh to show "built on gnosh" with a link to https://gnosh.ai, and the
    GnoVM's (gno.land's) asks an interface that includes the GnoVM to show
    "built on gno.land" with a link to https://gno.land; gnosh showed
    neither except at the end of gnovm_list. Now the header that opens a
    session ends with one line: https://gnosh.ai · built on gno.land
    (https://gno.land) · /license, the addresses as links where the
    terminal takes them. /license (also /licence) shows the copyright, that
    there is no warranty, the licence and where to read it, and both
    credits: the "Appropriate Legal Notices" of the licence's section 0, so
    that under its section 5 d a work made from gnosh shows them in its own
    interface too. gnovm_list ends with gnovm.AttributionNotice, whose
    words it already printed. Asked by Jae on 18 November 2026: "make sure
    the new gnosh license is good, strong attribution too".

140. **Fallback models, as Claude Code's fallbackModel.** `"fallbackModel"`
    in gnosh.json (one "<provider>/<model id>", or a list of up to three;
    a project's waits for trust as its "model" does) and `--fallback-model`
    (comma-separated, bare names as -m takes them) name the models a turn
    goes on with, in order, when the session's model cannot answer. What
    hands a turn on: a server that cannot be reached or says it is not
    ready (gateway errors, a connect timeout), one that says it is too
    busy (overloaded), one that does not have the model (404), at once
    and with no wait; and any other trouble that may pass, once the retry
    policy's wait for it runs out. A limit on the person's requests, a key
    that is refused, and a request too long or that no model would take
    are not: another model would meet them too. Claude Code leaves
    "transport errors" out; gnosh counts a server that cannot be reached
    as unavailable, since each model of gnosh's lives on a server of its
    own. The switch holds for the rest of the turn (its sub-agents start on
    the fallback, and keep the list), and the next turn asks the session's
    model first again; a model chosen with /model outranks it. A fallback
    is made (its client, its window) only when a turn first needs it, so
    that one whose server is down costs nothing until then; one that
    cannot be made, whose context window is smaller than the session
    model's (the conversation would be cut to fit it), or that is on the
    same model server of one's own as the model that failed (no help when
    that server is down or busy, and a router that holds one model at a
    time would unload the one in use to load it), is passed over with a
    warning. The session's thinking level carries over where the
    fallback has it. The person is told in one line, as a retry event that
    names the fallback (RetryEvent.FellBack), not counted as a retry.
    Summaries fall back too. When every fallback fails as well, the last
    one is waited for as any model is. Not built: a fallback chain for
    the small model's side jobs (they fall back to the session's model
    already).

141. **/subtask, as Claude Code's.** `/subtask <task>` copies the
    conversation into a run in the background that works on the task, as
    /fork does, and when the run ends what it printed comes back to this
    conversation: a task notification (prompt text subtask-ended, the
    answer marked as the run's own words and defused) that starts a turn
    of its own when the session is idle, so the model tells the person
    what came of it. The run is named after the task's first words (six
    at most), as Claude Code names its fork. In Claude Code /subtask is an
    in-session forked sub-agent with a panel under the prompt; gnosh's is
    a run of its own (its questions wait in the agent view, ← ← shows it
    meanwhile), because gnosh's background runs are where its agent view
    already lives, and a fork in the same process would take turns with
    the session on one model server and push its cache out. A report is
    the last 40 lines the run printed, and says so when it was cut. As
    with /fork, a running turn must end first. Not built: the model
    starting a fork of its own (Claude Code's fork mode).

142. **The review of fallback models and /subtask (1835835, fd5fc19).** A
    fresh-eyes review found nine problems, fixed together:
    - A fallback whose window nobody knew was let through and taken to
      hold UnknownWindow tokens, so a long conversation was slid and
      summarized for good to fit it. Now a fallback must hold the
      conversation as it stands (the failed request's size under its
      compaction point, a window nobody knows counted as UnknownWindow;
      for a summary, which is there to cut, under its hard limit). A
      smaller window that does hold it is used.
    - A fallback that was down made a turn fail sooner than none: it was
      called not running after 90 s, and the session forgot that its own
      model had been answering. Which models have answered is now kept
      per model for the session, and when every fallback fails the turn
      goes back to the session's own model and waits for it as it would
      without them.
    - The same-server guard compared only with the model that failed
      last; every model server of one's own that failed in a turn is now
      remembered.
    - What a provider's type adds to a request was looked up by the
      model's id alone, so a fallback with the same id on a provider of
      another type changed the session model's requests (its thinking
      switch was lost). It is kept by provider and model; the agent's
      Outgoing hook is given the provider's name.
    - Making a fallback read the providers' lists without switchMu, as
      every other maker of a model holds it.
    - Esc while a fallback was being made ready ended the turn as failed;
      it is a stop. The line says "getting the fallback model … ready".
    - A summary handed to a fallback said "No summary was made"; it says
      nothing, and the fallback's own summary reports.
    - A panic at a turn's very end left the fallback in force.
    - /subtask: a run that began and ended between two looks was said
      not to have started; how it ended is read from its log.
    Not changed: a /subtask whose session closes or restarts before it
    ends does not report back (its answer stays in its own session and in
    the agent view).

143. **The review of /rewind and its file copies.** A fresh-eyes review of
    the rewind (checkpoint.go, rewind.go) found five ways to lose work or
    put back the wrong thing, fixed together:
    - A rewind of the conversation alone left the copies kept under the
      prompts it took out unread, so a later rewind of the files put back
      an abandoned attempt (or nothing). The files' rewind now goes by the
      prompts of the session's file in order, those taken out of the
      conversation among them: their changes were made to the files all
      the same.
    - A rewind overwrote or took away what a file held without looking:
      the person's own edits, or a command's, since the model's change. It
      now keeps what a file holds first, when that is not what is put
      back, in the session's checkpoints/replaced, and says where. (Claude
      Code overwrites; gnosh never loses work to a rewind.)
    - A sub-agent's changes were kept as the session's own, so a rewind
      took back work a worktree sub-agent had left for the person, or
      wrote into a tree already removed. As in Claude Code, they are not
      kept.
    - A prompt typed while a turn ran was cut back to where it was typed,
      with the turn's changes made meanwhile left in place and shown as
      interrupted. The cut is where the prompt went out; a prompt that
      waited is marked as taken back, so that a session opened again does
      not send it.
    - The copies were looked for under the folder gnosh was started in,
      not the session's own: a session resumed from another folder, and a
      branch (/branch, /fork, /subtask, --fork-session), found none. They
      are found by the session's file, and a branch also reads the copies
      of the session it came from, under the prompts it took over.
    Also: a copy that fails is noted, so a later one is not taken for what
    the file held before; copies are named afresh, not by count; a rewind
    of the files waits for the sub-agents in the background; the files of
    "/rewind N all" are put back only once the rewind is written and the
    background work is stopped; and the menu is worked out from one read
    of the session's file. Not changed yet: copies are never pruned, and
    fileCheckpointingEnabled is not built.

144. **File copies: the 100 most recent prompts, hard links, and
    fileCheckpointingEnabled.** As Claude Code keeps the file snapshots of
    its 100 most recent checkpoints, a session keeps the copies of its 100
    most recent prompts (checkpointPrompts): when a new prompt's first copy
    is made, the copies of older prompts are deleted, the index is written
    again without them, and the prompts are noted first in the store's file
    "pruned". A rewind of the files to before one of those (or to before a
    prompt of a branch's origin whose copies were let go) is refused, and
    the list says so: putting back what is left would be wrong, not just
    short. A file with another name (a hard link, as pnpm makes them) is
    not put back, as in Claude Code: a new file in its place would cut it
    off from its other names. "options": {"fileCheckpointingEnabled": false}
    keeps nothing; a rewind of the files then says why it cannot be done.
    Not built: Claude Code's retention sweep (cleanupPeriodDays): gnosh
    keeps a session's copies as long as its file.

145. **The security review of the file tools' paths.** A fresh-eyes review
    of how Read, Write, Edit, Glob and Grep are judged outside Bash found:
    - Grep and Glob left Read deny rules out of a folder they walked: the
      gate judged the folder, and a deny on Read(./customer-data/**) or
      Read(*.pem) did not keep those files' contents or names out of a
      search of the project. fstools.Options.ReadDenied (the engine's
      ReadDenied, which nothing called) now leaves them out of both walks,
      by the path as written and as reached.
    - Write and Edit judged one path and could write another: they found
      the file again when they ran, so a folder on the way swapped for a
      link between the question and the run (by a command in the
      background) sent the write where nobody was asked about. A call is
      now held to the real path it was described with, and refused when
      that changed.
    - A protected file or folder that is a link (a settings file linked
      from a dotfiles repository) did not protect what it leads to, which
      is the file the settings are read from; it does now. The files
      --mcp-config names are protected, as in Claude Code. A work tree
      named as a protected folder (.git) gets no work tree's exemption.
    - The shared resolver read a dangling link's target from its folder as
      written, not as reached: "d/dang" with d a link was read from the
      text's folder. It now resolves the link's folder first.
    Not changed: Write and Edit say that a file outside the project exists
    when they cannot run (its content is not shown).

146. **The security review of the web tools.** A fresh-eyes review of
    WebFetch and WebSearch found the fetching sound (addresses checked
    after DNS on every connection, redirects on the same host only, domain
    rules matched label by label, everything a page says marked untrusted)
    and two things to change:
    - Turning a page into Markdown had no bound of its own: the download
      is cut at 10 MiB, but every paragraph mark copied all that was
      written so far, and every list item was indented by every list left
      open, so a page of about 8 MB grew to gigabytes or kept a core busy
      for many minutes, after Esc too. The converter now ends a line and a
      link in place, keeps at most 16 lists and 64 links open, and stops
      at 4 MiB of text, many times what a model is given of a page, saying
      so at the end ("The page went on past what gnosh turns into text").
      A page made to blow up now costs what its size does: under a second.
    - The WebSearch question showed webSearchUrl as written, password and
      all, and only the first 80 bytes of the query. It now shows the
      server as the rest of gnosh shows an address (no user name,
      password or query string), and the whole query; a query longer than
      the question shows (2000 bytes) is refused with the reason, so
      nothing sent is hidden from the person asked.
    Not security, but found there: a self-closing <svg/>, <iframe/> or
    <button/> opened an element to skip that never closed, and the rest of
    the page was lost. Not changed: the addresses the fetcher refuses leave
    out a few rare ranges (198.18/15, 192.0.0.0/24, NAT64); every fetch is
    https with a certificate checked against the name asked for.

147. **Esc Esc and /resume read their lists away from the loop.** The list
    of prompts to go back to is read from the session's file (and the
    file copies' index), the list of sessions from the folder's files: on
    a long session, or a folder of many, the screen stood still meanwhile.
    Both are now read away from the loop. What is typed meanwhile is held
    for the list, as had it come at once: Down and Enter pick in it. (A
    first version let those keys through to the box, where Down took the
    last prompt back and Enter sent it again; the test with the real
    program caught it.) A question or a turn that comes first lets the
    list go, and so does Ctrl+C, with a line that says how to open it
    again; what was typed for it is dropped, never given to the box or to
    the question.

148. **claudeMdExcludes.** Claude Code's settings key (its settings
    reference, read 2026-11-19): instruction files never to read, as
    glob patterns or absolute paths matched against their absolute paths,
    for a monorepo's other teams' CLAUDE.md files. gnosh's reader of
    instruction files could already leave files out (Options.Exclude) and
    nothing set it; "options": {"claudeMdExcludes": [...]} now does, with
    the patterns rule files use: "**/vendor/**/CLAUDE.md", a folder for
    all under it, or a bare name for every file of that name. The entries
    of every file add up. A project's file holds them until the folder is
    trusted, as a removal: they can leave out the person's own GNOSH.md,
    which may hold the rules they keep the agent to. A pattern the reader
    cannot use stops startup at its line, since it would keep nothing
    out; the app checks it (the config package imports nothing of
    gnosh's).

149. **showTurnDuration and respectGitignore.** Claude Code's settings keys
    (its settings reference, read 2026-11-19), as options. showTurnDuration
    false leaves out the line under a turn that ended as it should ("done
    in 42s · 2 replies · …", Claude Code's "Cooked for …"); a turn that
    was stopped or failed still says so, since that line is news. It is
    the person's own (not from a project's file), as the other ways the
    conversation is shown are. respectGitignore false has the @ file
    picker list what the ignore rules name too: git's list without
    --exclude-standard, or the walk without the ignore files (dot folders
    stay out). Both go to the interface in the session's info, and
    /config changes them at once: the session's info gives an option as
    /config last set it (liveBool), and the interface reads the info
    again after every command; the picker lists the project again when
    the setting changes.

150. **The security review of the hooks core: the three high findings.**
    A fresh-eyes review of the hooks and their callers found that a
    hook's approval cannot override a deny or ask rule, a protected path,
    a safety check, plan mode or a tool's own confirmation; the holes
    were around that.
    - Monitor runs any shell command, and hooks are matched by the tool's
      name, so a Bash hook never saw Monitor's commands: a hook refusing
      `git push --force` let Monitor run it. As in Claude Code (its hooks
      page), a Bash hook now guards the commands Monitor starts, on
      PreToolUse and PermissionRequest: it is given each as a Bash call
      (tool_name Bash, the command and its description), it can refuse or
      ask, and its approval and changed input count for nothing there.
      After the command (PostToolUse) it does not run for Monitor. The
      other guards (MultiEdit for Edit, gnosh's own tools for Claude
      Code's) keep their rule: their changed input is used.
    - A PermissionRequest hook's approval ran a call the engine marks as
      critical (a removal that may take what cannot be got back), with
      no person there under -p. Only the person approves one, as no rule
      does; the hook's refusal still stands, and its approval puts the
      question to the person with a line that says why.
    - The folders added to a session (--add-dir, /add-dir,
      additionalDirectories) were searched for sub-agents, skills,
      commands and output styles once the main folder was trusted, and
      their hooks and tool grants went live with nobody asked. A folder
      is added for its files; its definitions are now read only when it
      is trusted itself (gnosh trust, run there) or lies in the trusted
      project, and the session says what it leaves out. Claude Code runs
      an agent file's hooks only once the folder it came from is trusted.

151. **The security review of the hooks core: the medium findings.**
    - Hooks configured twice were run once by the line a person reads of
      them, which cuts a question short and leaves out an MCP tool's
      input and an address's headers, and the first copy's way of running
      won: an async copy listed first turned the blocking guard into one
      that decided nothing. One handler now runs once only when it is the
      same in all of that, and runs the same way.
    - The file tools' paths reached hooks as the model wrote them, so a
      hook guarding $CLAUDE_PROJECT_DIR/secrets let "secrets/key" through.
      Hooks are now given the path absolute and clean, from the call's
      folder and with ~ the home folder, as Claude Code gives them; the
      tool reads the same file from the path as written.
    - A PermissionRequest hook's approval stood though another hook could
      not check, while the warning said the question went to the person.
      A hook that could not check now leaves the question to the person.
    - The SubagentStart, SubagentStop, PostToolUse and PostToolBatch hooks
      of a sub-agent with a work tree ran in the session's folder: a "the
      tests must pass" hook tested the person's checkout. They run in the
      sub-agent's.
    - A PostToolUse hook's replacement of a call's output skipped the
      filter every result goes through (gnosh's own marks broken, the size
      cut). It goes through it now.
    - A PostToolUse hook that did not finish (it ran out of time, was
      killed, printed past the cap, or was cut short when the turn
      stopped) left the output as the tool gave it, so a hook there to
      hide a key showed the key when it slipped. Such output is withheld
      now, with a line that says why. A hook that ends with a status of
      trouble on purpose leaves it as it is: a formatter that fails on a
      file must not hide every result.
    - The question about a call a hook changed showed the call as the
      model wrote it (its diff, its command); it shows the call as it
      will run.
    - In a folder that is not trusted, a hook of the person's own that
      runs a program in the project ("$CLAUDE_PROJECT_DIR"/.claude/hooks/
      check.sh) ran whatever a repository just cloned put there. It is not
      run there now, and counts as a hook that could not start: a guard of
      that kind blocks what it would have checked.

152. **The security review of the hooks core: the low findings.**
    - What a PreToolUse hook adds for the model (additionalContext) was
      read and never given to it. It goes with the call's result now,
      whatever was decided (agent.Decision.Context).
    - "once" in an agent's definition took the agent's guard away after
      its first run; Claude Code reads "once" in skills alone, and so does
      gnosh now.
    - A hook's rewrite of a question call made the answers in it the
      person's, even answers the model wrote itself and the hook left as
      they were. Only answers the hook changed or added are.
    - A tool matcher that names an MCP server alone ("mcp__memory") fits
      none of its tools; the check at startup says so, and names the one
      that would ("mcp__memory__.*").
    - The exec form ("args") looked its program up in the empty and
      relative entries of PATH too, which are the folder the hook works
      in; they are passed over, as Go's own lookup does.
    - The package's docs said the hook types other than "command", "args"
      and background hooks stop startup; they no longer do.
    "continue": false on SessionStart, SubagentStart and PostCompact was
    recorded as having ended the turn while nothing ended: built in 154.
    Not changed: the shell command of a call is also given to hooks as
    GNOSH_TOOL_INPUT_COMMAND, and one past 128 KiB is more than Linux lets
    a program be given in its environment, so no hook can start for it
    and a guard blocks the call. To leave the variable out would let a
    command padded past the limit by those hooks that read it; a call
    that big is better put another way (a file the model writes first).

153. **What a command leaves running is looked at twice.** When a Bash
    command ends, the processes still in its session that let go of its
    output (a server started with cmd >log 2>&1 &) are kept on the books
    and reported. A clean-copy CI run on a busy devbox reported
    `(sleep 0.1; echo late) &` as left running: the subshell had closed
    the output as it ended and was seen before it was gone. As the
    pruning of the books already does (pruneDetached, since 7fd75ab),
    the session is looked at again 50 ms later, and only what is there
    both times, with the same start, was left running.

154. **"continue": false at the start of a session or a sub-agent, and
    after a summary.** Claude Code: with "continue": false, "Claude stops
    processing entirely after the hook runs", on every event that takes
    it. The hooks package read it so on SessionStart, SubagentStart and
    PostCompact, and the session went on regardless. A moment can now
    return agent.StopWork: the session's start refuses the prompt it
    stands before and every one after it, with the hook's words (a run
    exits 1 and sends the model nothing); a sub-agent's start refuses its
    task, and the model that started it reads why; the end of a summary
    made in a turn (PostCompact, or SessionStart with "compact") ends
    that turn as halted, once what it gave back is kept. After /compact
    no turn runs, and nothing is ended. Also found on the way, in CI: a
    test of background runs stood in for gnosh with `sh -c "sleep 30" id`,
    and dash puts sleep in its own place, so the command line lost the
    session's id on a busy machine (the stand-in now keeps it); and the
    agent's stress test takes GNOSH_STRESS_SEED to play a seed again
    (one run in a full suite saw a turn end with a request still out;
    thirty more runs did not, and the seed alone does not bring it back:
    a race of the loop's, left open).

155. **A turn stopped while a fallback is made ready ends with its request
    closed.** The agent's stress test saw a turn end with a request still
    out (154). The path: the model fails, a fallback is being made ready
    (fallbackModel, 140), and the person stops the turn; the loop gave
    back no reply, as after a retry's wait, but no retry had closed that
    request, so no reply event came for it, and an interface waiting on
    one waited for good. The failed try is now closed as stopped, with an
    empty reply (what had arrived of it is still not kept), for a turn's
    request and a summary's alike. TestStopWhileAFallbackIsMade checks the
    order of the events and catches it every time.

156. **The security review of sub-agents: the first three findings.** A
    fresh-eyes review of the sub-agent machinery found no high finding;
    the first of its medium ones fixed here:
    - Agent(kind) deny and ask rules compared the kind as the model wrote
      it, while the tool leaves blanks off and takes none for the
      general-purpose kind: "" and " Explore" went past
      Agent(general-purpose) and Agent(Explore), and past hooks' "if"
      filters. The engine now reads the kind as the tool does.
    - A definition's tools and disallowedTools were compared by name as
      written, so Claude Code's old name Task did not take the Agent tool
      away (a read-only kind from ~/.claude/agents with
      `disallowedTools: Write, Edit, Task` could still start a sub-agent
      with every tool), and under --agent, `tools: Task(worker)` took it
      away entirely. Old names count as the tools they name now.
    - The agent and skill definition files were read whole once their
      size was small: a cloned repository's .claude/agents/a.md that is a
      link to /dev/zero or a terminal hung gnosh, or ran it out of memory,
      at startup, in a folder nobody trusted (the count of what an
      untrusted folder holds reads them). Only plain files are read, and
      never past the size cap.

157. **The security review of sub-agents: the rest of its medium findings
    and one low.**
    - A sub-agent's permission question looked like one of the
      conversation the person is in ("Allow Bash?"), though a background
      sub-agent may have read a hostile page. It now says who asks: "The
      sub-agent Explore asks: Allow Bash?", and for one in the background
      what it was started for. Its bare "No" says that it stops that
      sub-agent. In the plain front end a second question took the place
      of the one being answered, which then waited for good, and the
      answer typed for it went to the other; questions wait their turn
      there now.
    - A background command a sub-agent started announced its end to the
      session's model, in gnosh's words, with what the sub-agent wrote of
      it (its description) in them, and the sub-agent, told it would hear,
      never did. The command now knows the sub-agent that started it: while
      that sub-agent works, the news is its own; once it has ended, the
      session's model is told the command ended, by its id and output
      file, without the sub-agent's words.
    - The MCP tools --agent's list leaves out were left out of the
      session's tools, and given to its sub-agents all the same. They are
      left out of both.
    Not changed yet: a skill's allowed-tools hold in the shared engine
    until the person's next prompt, for a sub-agent running then too (a
    sub-agent's own preloaded skills take the same path, so the grant
    cannot simply be refused to sub-agents); and an Agent call's model may
    name a model of another provider the person set up. TaskStop in a
    sub-agent can stop any background command of the session, and
    definition files pass over keys they do not know (a "disallowedtools"
    typo is lost without a word).

158. **A definition's setting in another letter case is not passed over.**
    Agent definitions pass over keys they do not know, as Claude Code's
    do, so that a key from a newer Claude Code does not stop gnosh. One
    that gnosh knows written in another letter case ("disallowedtools",
    "permissionmode") was passed over too, and the agent ran without what
    it was meant to take away (the sub-agents review's note). Such a
    definition is now not used, and the files that hold no usable
    definition (/status, gnosh doctor) say why.

159. **CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH and
    CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS.** Claude Code's variables (its
    environment reference, read 2026-11-19): how many layers of sub-agents
    there may be below the conversation (3; 1 turns nesting off), and how
    many may run at once before the Agent tool refuses another (20). Each
    takes a whole number above 0 in plain digits, and anything else is
    passed over, so a limit can be moved and not taken away. gnosh had
    both limits fixed, and the second counted only sub-agents in the
    background; it counts every one that runs now, as Claude Code's does.

160. **A sub-agent's TaskStop stops only what it started.** TaskStop in a
    sub-agent could stop any background command of the session and any
    sub-agent running in the background (the sub-agents review's note):
    one that read a hostile page could stop the person's build or server.
    A background command now knows the sub-agent that started it (157), so
    a sub-agent's TaskStop refuses a command it did not start, and refuses
    every background sub-agent (a sub-agent starts none). The session's
    own TaskStop is as it was.

161. **The "No" of a background sub-agent's question says what it stops.**
    The screen sets a permission question's bare "no" out as its own row,
    "the call does not run, and the turn stops". For a sub-agent in the
    background it stops that sub-agent alone (157), and the row said the
    turn. The question's data now says what a bare "no" stops when it is
    not the turn (QuestionData.Stops), and the row says it. Also a new
    box check, subask: the model has a sub-agent run a command, and the
    question says which sub-agent asks (passed on box 1 with Qwen,
    a4ef214).

162. **The sign-in's listener is shut down gently.** An MCP server's
    sign-in waits for the browser to come back with a code; the code goes
    to the sign-in before the page "Signed in" is written, and the sign-in
    then closed the listener at once. A CI run under load saw the browser
    get nothing (EOF): the person would have seen an error in the tab of
    a sign-in that worked. The listener now shuts down gracefully, with
    what does not finish in two seconds closed.

163. **The review of compaction: the three high findings.** A fresh-eyes
    review of how gnosh keeps a conversation within the model's window
    found that no tool call loses its result across a summary or a slide,
    and that a summary that fails leaves the conversation as it was; and:
    - One tool result could fill most of a small window: Read gives up to
      25,000 tokens, which on a model with a 32K window is over the point
      where a summary is made. The summary leaves the file out, the model
      reads it again, and the window fills again, a summary every round;
      past the room a summary leaves, nothing could be sent at all, and
      the session stood still until /rewind. A tool's result now takes at
      most a quarter of the model's window, cut with a line that says how
      much there was and to read the rest in parts. On a large window the
      tools' own limits are smaller, and nothing is cut.
    - Any text was taken as the summary, and the first try offers the
      session's tools: a reply "Let me check the failing test first." with
      a Read call would have stood for the whole conversation. A reply
      that calls a tool, or stops for one, or refuses, is not taken; the
      try without tools follows.
    - The summary reached the model as a plain message on the user's side,
      the person's voice, with gnosh's marks in it live, though the model
      wrote it from a conversation that held pages and tool output. It now
      comes after gnosh's own line, in its marks, that says the model
      wrote it and that what it quotes of files, pages and tool output is
      data; and gnosh's marks and chat-template tokens in it are broken,
      as in a tool's output.

164. **The review of compaction: three of its medium findings.**
    - What goes out with a prompt (an @-mentioned file, up to ten of 100
      KB; the environment; what a hook added) was worked out after room
      was made for the prompt, and counted only then: on a small window
      the first round's summary took the new prompt, unanswered, with the
      rest. It is worked out first now, and counted when room is made.
    - After a summary the file tools still took the files the model had
      read as read, though the model no longer saw them: Write and Edit
      let it change a file from a copy it could not see. Every file is
      forgotten at a summary, as the ones in a slide are, and must be read
      again before it is changed.
    - A hook that failed after a summary (a SessionStart one with the
      source "compact") took with it everything the session is given back
      then: the instructions it started with, its kinds of sub-agent, its
      skills, for the rest of the session. What was made is given back,
      with a word on what could not be.
    Left: the session is not given back the files it was working on, nor
    a fresh account of its environment, after a summary (Claude Code reads
    the files again); the slide protects gnosh's own hidden prompt after a
    nudge rather than the person's; a large pasted prompt can turn
    automatic summaries off as "not helping"; the trimmed fallback leaves
    out the first message even when it is the last summary.

165. **The review of compaction: two more of its findings.**
    - A slide keeps the prompt the model is answering and all after it.
      After a nudge, a driver's objection or a checkpoint, the newest
      prompt is gnosh's own, and a slide that kept only that one took the
      person's words before it with the rest ("handle the error case;
      don't touch the tests"). It keeps the turn's newest prompt that
      came through the queue, the person's (or a /loop's), and what follows.
    - A summary that left the conversation over the point turns automatic
      summaries off for the session, since another would not help. What
      was over could be the prompt about to go out, a long paste no
      summary makes shorter, and then summaries went off for good though
      the next one would have helped. A summary is now judged by what it
      left, without the prompt about to go out.

166. **After a summary the model is told where the conversation as it was
    is.** The line gnosh gives after a summary had a place for the
    session's file, for what the summary left out ("The conversation as
    it was is in …: read from it only what you need"), and nothing filled
    it, so the line went out without it (the compaction review's note).
    It names the session's file now, as Claude Code names its transcript
    after compacting. (A sub-agent shares the session's words and is
    pointed at the session's file.)

167. **A summary that has to leave out the earliest part keeps the first
    message.** When neither the session's own request nor the bare one
    fits, the summary is asked for with the earliest part left out, cut
    at one of the model's own messages. The cut took the first message
    with it, and that is the task as it was set or, after a summary, the
    summary all the rest builds on: a second summary forgot the first.
    The first message is kept now, with the note of what was left out
    after it ("Part of this conversation is left out here…"); only when
    no asking with it fits, a first message too big to keep (a long
    paste), is it left out as well (the compaction review's note).

168. **A summary gives back the files the model was working on.** A
    summary takes every copy of a file out of the model's view, and since
    the compaction review no file counts as read after one: the work went
    on with a Read of each file it had in hand. As Claude Code does after
    compacting, the files the session read or wrote last (up to five, the
    most recent first) are read again as they now are and given back
    after the summary, and count as read, so that an Edit of one needs no
    Read first. They keep to a tenth of the model's window (at most 50K
    tokens, 5K for one), so that what the summary made room for is not
    filled again at once; a file too long, a picture, or one the budget
    has no room left for is named instead, to be read again before it is
    changed. Read deny rules hold, and a file that is gone is passed
    over. The person reads which files were given back.
    - What the after-summary step made is kept when the turn is stopped
      while it runs, as the summary is, with which it goes; it was
      dropped, with the instructions and skills given back. When the step
      is stopped, the files it read again do not count as read, since a
      step that does not finish in time is gone on without.
    - A sub-agent forgets what it read at its own summary, as the
      session does: it kept counting files as read after it lost them.

169. **Two MCP servers that connect at once no longer lose one's tools.**
    Each server, once connected, offers the model the tools of every
    server connected by then, and the list was taken and offered in two
    steps: two servers connecting together could have the older list
    offered last, in place of the newer, and one server's tools were gone
    until the next change of tools. A session running as an agent with a
    server of its own beside a file's lost the agent's in one run in 300
    (the clean-copy CI caught it twice; with the fix 1000 runs in 1000
    kept both). Taking the list and offering it are one step now.

170. **Claude Code's language and includeGitInstructions.** language (a
    name, as the person writes it: "japanese") asks in the system prompt,
    after the output style, for replies in that language, as Claude Code
    passes it to the model; code, commands, file names and quoted text
    stay as they are. It holds under --system-prompt too, being the
    person's choice and not gnosh's words. A sub-agent's system prompt
    does not carry it: its report is read by the session's model, which
    replies. includeGitInstructions false leaves git out of the block
    ahead of a session's first prompt; gnosh says there only whether the
    folder is a repository and its branch, read from files, and has no
    commit or pull request instructions to leave out besides. Both are
    read as a session starts.

171. **Claude Code's bashOutputMaxChars.** The characters of a command's
    output the model reads, which BASH_MAX_OUTPUT_LENGTH already set:
    from 1 to 128,000 as Claude Code takes it (the variable goes to
    150,000), 30,000 when neither is set. A longer output is shown as its
    start and its end around a line that says what was left out, and is
    kept whole in a file. The variable, when set, wins, as the
    environment outranks the files; a failed command's output keeps its
    own bound (10,000), never more than this.

172. **The review of the MCP client: its transports.** A fresh-eyes
    review of gnosh's MCP code (2026-11-19) found calls that could wait
    for ever, and fixed here:
    - A server at a URL gives a request's answer with the request: as
      JSON, or in a stream of events (gnosh opens no stream of its own
      where a later answer could come). A request answered with 202, with
      JSON that held no answer to it, or with a stream that ended before
      its answer (the server restarted, a proxy cut it) waited for ever
      when the call had no timeout, which is the default; a run never
      ended. Each now fails at once as unreachable, so the server is
      connected again and the model is told that whether the call ran is
      not known. One event of a stream is held to the size of an answer
      (16 MiB): a larger one ends the stream, where it grew without end.
    - A server gnosh starts that stops reading its input held a call
      larger than the pipe, and every message after it, for ever, past
      the call's timeout and Esc. The write gives up with the call now,
      or after two minutes; one that gave up partway leaves half a line
      in the pipe, so the connection ends there.
    - A server that exited while a process it left outside its group
      held its output open kept its calls waiting, and closing it (at
      the end of a session or a sub-agent, /mcp) waited for that process
      to let go. Two seconds after the server exits its output is
      closed; what it left in its group is stopped as it closes.
    - An answer that came as the connection ended could be sent on a
      closed channel: answers are handed over under the lock now.

173. **The review of the MCP client: the session's side.**
    - A connection is made for the server as the caller marked it, and
      not taken when the server was disabled or connected again since.
      The connection read the server's generation as it began, after the
      caller had let go of the lock and closed the old connection (up to
      five seconds for a server at a URL): /mcp disable in that time was
      undone, two reconnects could both be taken (and one connection
      never closed), and a sign-in that ended after /mcp disable, or /mcp
      logout, connected a disabled server again. A sub-agent's own server
      is the sub-agent's before it connects, so that one that ends
      meanwhile closes it.
    - A flood of notices that a server's tools changed ran a listing of
      them each on a goroutine of its own; one runs at a time now, and
      once more after the notices that came while it ran. The server's
      own requests (pings) are answered 32 at a time; more are refused.
    - Two of a server's tools whose names come to one (listed twice, or
      "gr.eet" beside "gr_eet"), or two servers' ("a" with "b__c" beside
      "a__b" with "c"), had every offer of tools to the model refused, so
      that servers that connected later were never offered and disabled
      ones stayed offered. The first of each name is offered, and the
      person is told of a server's own.
    - A tool's description and input schema are the server's words: they
      reach the model with gnosh's own tags broken, as what the tool
      returns does.
    - The own servers of sub-agents still running end with the session.

174. **Claude Code's fileSuggestion.** A command of the person's own
    offers the paths an @ mention completes to, in place of gnosh's own
    search (for a big repository with an index of its own):
    {"type": "command", "command": ...}. As Claude Code runs it, it is
    given {"query": what was typed after the @} on its standard input
    and the variables a hook has (CLAUDE_PROJECT_DIR among them), prints
    a path a line, of which fifteen are offered in its order, and is
    given five seconds; what it started ends with it (its process
    group). It runs in the background, one at a time: until it answers,
    the last answer's paths that still hold what was typed are offered,
    and the list fills in when it answers, with no key pressed; what was
    typed meanwhile goes to it next, the latest only. Answers are kept
    until a turn ends (a turn may make files), and a failure is told
    once. It is a program to run, so a project's waits for trust, as a
    hook's and the status line's do, and safe mode and disableAllHooks
    turn it off, which /status says.

175. **Claude Code's attribution, and includeCoAuthoredBy.**
    {"commit": ..., "pr": ...}, or false: the lines the model ends a
    commit's message and a pull request's description with, asked for
    in the system prompt of the session and of its sub-agents (which
    commit too), with the person's own instructions about it first, as
    Claude Code tells the model. gnosh signs with nothing of its own
    (Jae's rule for his repositories is no agent trailers), so with
    nothing set nothing is said. sessionUrl is taken and adds nothing: a
    gnosh session has no web address to link. includeCoAuthoredBy,
    Claude Code's older word for leaving its own attribution out, is
    taken and changes nothing.

176. **An MCP tool's name longer than the model's APIs take is
    shortened.** OpenAI's API refuses a request whose tools hold a name
    longer than 64 characters (Anthropic's takes no longer one either),
    and mcp__<server>__<tool> passes that with a long server name and a
    long tool name: every request of the session failed (the MCP
    review's note). Such a name keeps its start and ends in a short hash
    of the whole, so that two long names that begin alike stay apart;
    the server is still called by its own name. A rule for that tool
    names it as offered, which the permission question shows.

177. **A session file from outside gnosh's folder goes on as a copy.** -r
    takes a session file by its path, and opened it where it was, for
    writing, with its checkpoints taken from beside it. A fresh-eyes
    review of the session store (2026-11-19) found what that let a
    shared file do: a checkpoints index beside it could make /rewind
    write, delete or copy any file the person can (its entries were not
    checked, and a copy's name could climb out of the folder); its tool
    results, summaries and notes reached the model as gnosh's own words,
    its prompts as the person's standing rules; its scheduled tasks came
    back and fired; and the person's own conversation was then written
    into the file. Now a file outside gnosh's folder (links followed) is
    copied into a new session of gnosh's own and gone on with there: the
    file is never written, nothing beside it is used, what would steer
    the session (scheduled tasks, goal, todos, the kind it runs as) is
    left out, and every word in it but the model's replies is read as
    data. The copy holds the whole conversation itself, so a /fork of it
    or -c later keeps it (a copy that pointed back at such a file found
    nothing when opened again). The person is told. A checkpoints index
    is read only with whole paths and plain names for its copies, and a
    session's id is checked before it names a folder.

178. **The session store review's smaller findings.**
    - A redaction puts a new file in a session file's place. One that
      did so between another gnosh's open and its lock left that gnosh
      holding the old file: what was redacted, and every entry it wrote
      lost. The file at the path is checked to be the one locked, and
      opened again when it is not.
    - A write that failed partway (a full disk) left the whole lines
      before the tear in the file, where they came back as entries the
      session never had: the file is cut back to its size before the
      write, and only when that fails is the torn line left to be passed
      over.
    - The newest session, which -c goes on with, is the one talked in
      last: a name given to an old session later (gnosh session rename)
      made it the newest.
    Left as they are: a kill between /cd's move of the file and its note
    of where to (the session is then found by its id alone), /resume N
    listing again as it is typed, and sessions read whole.

179. **A tool call written out as text is told so.** Qwen on llama.cpp
    now and then writes a tool call in its template's markup that the
    server does not take as a call, or leaves only the closing tag (a
    ship's smoke test ended on "</tool_call>"): the reply calls no tool,
    and the turn ended on it with nothing run; in a run (-p) the task was
    lost. A reply that calls no tool and begins with "<tool_call>" or
    ends with "</tool_call>" now gets a line of gnosh's own, as an empty
    reply does: it held a tool call written out as text, which runs
    nothing; call the tool, or answer in words. No count gives up, and
    the person is told at every third in a row. A reply that only speaks
    of the markup is an answer.

180. **The review of the shell tool.** A fresh-eyes review (2026-11-19)
    found no hole in where commands may run or what reaches other
    machines, and these:
    - A command of the person's (!) that gives gnokey a password on its
      standard input was refused, but only after the line was kept in the
      session's file (and so in the history Up brings back, in later
      sessions too) and sent to the model with what it printed: the
      refusal's own reason had already come true. It is refused before
      any of that now.
    - A working directory taken away (a checkout without the folder, a
      clean) kept every command from running, the model's and the
      person's own cd alike, with nothing to get out by. A command now
      runs in the nearest folder above it that is there, inside the
      session's folders, and the session moves there; a command that
      takes its own working directory away moves the session up as well,
      where the result said the shell had replaced itself.
    - The person's own !cd told the model the working directory had
      moved, and the session had not: the next command ran where it was.
      Their cd moves the session now, as one in the model's own calls
      does, with the CwdChanged hooks.
    - The file in which the shell says where it ended is read without
      following a link and without waiting on a pipe, only when it is a
      plain file, and only so much of it: a command that put a pipe in
      its place held the result up for ever, and a link to /dev/zero
      filled memory.
    - Folder names in the notes under a result are written as the
      stamp's are: a name with a line break in it could pass for a note
      of gnosh's.
    Left: background commands' output files and ended tasks are kept
    until the session ends, and a process left running after its
    command is not held to the output limit.

181. **A long session lets go of old ended background commands.** Every
    background command that ended was kept until the session ended: its
    output's start and end in memory (some 120 KiB) and its output file
    on disk (up to 64 MiB), so that a /loop running for hours built up
    both (the shell tool review's note). The last 100 that ended are
    kept; an older one is let go of, and its output file with it, unless
    something it left running still writes there. The model is told of
    each as it ends, with its file.

182. **The review of print mode.** A fresh-eyes review (2026-11-19) of -p
    and the stream-json protocol:
    - A stream-json message that begins with "/" and names no command
      ("/etc/nginx/nginx.conf fails to load, why?", or any "/" message
      with slash commands off) waited under no id: nothing was answered,
      no result line was written, and the run never ended. It is a
      prompt like any other now, as in the text path.
    - Every run that used Bash was taken for one with work in the
      background (a command that ended in the foreground counted), so
      in stream-json its result line was written before the structured
      output was asked for: --json-schema never reached the result, a run
      whose structured output failed exited 1 under a "success" line, and
      each such run waited a second more. Only work that went to the
      background counts now.
    - The turn the model is given when a run's end would stop what it
      left running (DECISIONS 117) is gnosh's own: its reply ("Restarted
      the server with setsid") took the place of the answer to the
      prompt as the run's result. The answer stays the result.

183. **--max-turns with stream-json input counts each message's turn
    afresh.** As Claude Code's: "a message still queued when the cap ends
    a turn starts a new turn with a fresh count". The count ran over the
    whole run, so after one turn reached the cap every later message was
    stopped before its first request (the print mode review's note). And
    the cap was asked before each request, after what the person had
    queued meanwhile was handed to the turn: the next message went into
    a turn the cap then stopped, and was never answered. The cap is
    asked after a round now, before anything queued is handed over (the
    agent's MoreRounds moment): the turn ends there, and the queued
    message goes out as a turn of its own. With text input the count is
    still the run's.

184. **A signal stops print mode wherever it is.** SIGINT, SIGTERM and
    SIGHUP were read only while a turn ran (the print mode review's
    note). While gnosh -p read what was piped in, ran a slash command
    given as the prompt, or asked for the structured output (three tries
    of up to ten minutes), a signal waited unread, so "producer | timeout
    60 gnosh -p" could not stop it short of SIGKILL, and the run later
    ended with its own exit code rather than the signal's. Each of those
    parts now runs under a watch that cancels it at the first signal; the
    run ends as one a signal stopped does: 130 or 143, and the result
    line in the JSON formats, written at once when no turn of the run's
    own is left to report.

185. **A run whose turns are spent stops its background work at the
    checkpoint.** gnosh asks the model, after ten minutes of idle
    waiting for background work, whether to wait on, where Claude Code
    stops what is left (DECISIONS 117's wait). With --max-turns spent, the
    turn that asked was stopped before its first request, the next
    checkpoint asked again, and a run whose background work never ended
    never ended either (the print mode review's note). When the model
    cannot be asked (its turns are spent, or the session is closing),
    the run says so on stderr, stops what is left, as Claude Code's wait
    does, and ends with the last turn's result.

186. **An MCP server's name keeps its tools apart from another's.** Tools
    are named mcp__<server>__<tool>, and a rule for a server's tools
    (mcp__github) reads as mcp__github__*: it covered the tools of a
    server named github__x or github_ too, so an allow for one server
    reached another that a project's .mcp.json added (the MCP review's
    note). And FitToolName, shortening a name to the 64 characters the
    model's APIs take, could cut through a long server name, so that a
    deny rule for that server covered none of its tools. A server whose
    name, as its tools' names write it, holds "__", ends in "_" or is
    longer than 48 characters is not used now: the configuration's
    problem says why and to rename it, and gnosh mcp add refuses the
    name. Two servers whose names come to one (a.b and a_b) still have
    the first one's tools offered.

187. **An MCP server is said to be connected once its tools are
    offered.** Its status read "connected" a moment before the model was
    given its tools, and a prompt typed in between went without them (a
    CI run under load: /mcp said connected, and the model's call found no
    tool of that name). The tools are offered first now; /mcp says
    connected after, and a disable in between still wins.

188. **/resume N opens the Nth session of the list as it was shown.** The
    number was looked up in the list made again when it was typed, so a
    session beside this one written to in between (a peer in the same
    folder, a gnosh -p) came first and was opened in place of the one
    picked (the session store review's note). The list /resume showed is
    kept; with none shown, a number is the place in the list as it
    stands.

189. **A session whose /cd was cut short is still listed.** /cd moves the
    file to the new folder's directory and then writes the move down; a
    gnosh killed between the two left a session listed with neither
    folder (its file in the one, its folder named the other's). The list
    now holds a session whose folder belongs in another directory with
    the folders of the directory its file is in; a session of another
    folder whose name only comes to the same directory is that folder's
    alone, as before.

190. **A sub-agent's retry leaves the session's reply alone.** When a
    request whose reply had begun to arrive is tried again, print mode
    throws away what had arrived of the reply. It did so for a sub-agent's
    retry too (a background sub-agent working while the session wrote
    its answer): the session's own half-written reply was lost, and in
    stream-json with --include-partial-messages its message was closed
    half written (the print mode review's note). Only the session's own
    main request's retry throws its reply away now.

191. **A stream-json message with priority "now" stops what runs when it
    arrives.** Messages are sent one at a time, each once the one before
    it has started its turn; the "now" message stopped the running turn
    only when its own turn came to be sent. With a message before it still
    waiting, the turn it was sent to stop ran on (a long one to its end),
    and the turn after, which started after it came, was stopped in its
    place (the print mode review's note). It stops the running turn as it
    is read now. The person's messages before it stay in line and are
    answered first: Interrupt drops only gnosh's own queued prompts.

192. **The result is the last line a run writes.** After the result line,
    the session goes on closing (SessionEnd hooks, the memory, what is
    left running), and what it said went out as before: in stream-json a
    notice became a system line after the result, which a reader takes
    for the start of another (the print mode review's note). Once the
    result is written, notices go to standard error alone.

193. **A provider's failures are given up on by the clock, whatever each
    try sent first.** The five minutes a failing provider is given
    (GoneAfter) were set back by the first piece of every reply. One that
    sends a little of each reply and then fails the same way each time
    (llama.cpp's "Failed to parse tool call arguments as JSON", a proxy
    that cuts at the same byte) was asked again every twenty seconds for
    ever, and gnosh -p never ended (the model client review of
    2026-11-19). The clock now runs from the first failure until a reply
    is whole; "answering again" is still said as the first piece comes. A
    reply that streams for longer than the five minutes and then fails a
    second time ends the turn, and the person says go on. A provider's
    own wait (Retry-After) is kept to up to BusyMax: a proxy that asked
    for a day had a run sleep a day without a word.

194. **"Overloaded" in a message no longer outranks a refusal's code, and
    a wait hint is read without recursion.** The word "overloaded"
    anywhere in an error body made it a busy service's, waited on without
    end, though its code said the request was refused as invalid (a C++
    "overloaded function" quoted back) or a proxy echoed a prompt holding
    it. The phrase is now read in the error's own message, after the
    codes that refuse a request and before a server's own. And the reader
    of "try again in 6m0s" called itself for each part, so an error of a
    million "1s" ran the stack out, which no recover catches; it reads up
    to four parts in a loop, in the first 4 KiB of the message (the model
    client review of 2026-11-19).

195. **Anthropic: no thinking is asked for the way each model takes it,
    and a default cap fits the model.** gnosh asked for no thinking with
    thinking {type: "disabled"} (/think off, MAX_THINKING_TOKENS=0, the
    side requests of a model with no known levels). Claude Opus 5.5 and
    the Fable and Mythos models refuse that value with a 400, and Claude
    Sonnet 5.5 takes only {type: "between_tools"}, so every such request
    failed. The Models API says which a model takes
    (capabilities.thinking.types.disabled.supported and
    between_tools.supported, read 2026-11-19 at platform.claude.com);
    gnosh now reads it when it lists the models and sends "disabled",
    "between_tools", or, for a model whose thinking cannot be turned off,
    no thinking field and effort "low". A model the list did not say
    anything of is asked with "disabled", as before. And a request with
    no cap of its own asks for at most what the model writes in one
    answer, as listed: the default of 64000 is above the 32000 of the
    older Opus models (the model client review of 2026-11-19).

196. **An image the model's server will not take for its size no longer
    wedges the session.** A conversation keeps the pictures its tools
    read, so a request a server refused for one was refused again on
    every later turn, until /rewind (the model client review of
    2026-11-19). Anthropic's API refuses an image over 8000 pixels a side,
    and holds every image of a request with more than 20 to a lower limit
    (its message names "many-image requests"), which screenshots it took
    before can be over: a session that read many was refused from its
    21st on (limits read 2026-11-19 at platform.claude.com, vision). The
    image fallback that leaves out what a server could not read now also
    leaves out, as too large, the images a refusal for size can be about
    (those the server has not taken before), and after a many-image
    refusal sends only the newest 20 images whole, the older ones as a
    line that says so, then and from then on. The conversation keeps
    every picture.

197. **An MCP tool whose input is not described as an object is not
    offered.** The model's APIs take a tool's input schema only as an
    object schema. A server that listed a tool with another type
    ({"type": "array"}) or a schema that is no object at all had every
    request of the session refused while the tool was offered, until the
    server was disabled. Such a tool is now left out, with a warning that
    names it; a schema that only leaves the type out gets "type":
    "object". As in Claude Code 2.1.328 ("an MCP tool with an invalid
    input schema ... is now left out and Claude is told why", changelog
    read 2026-11-19).

198. **A host whose login shell is zsh runs "[ a == b ]" as written.** A
    command on a declared host is eval'd by that account's login shell,
    which on a Mac is zsh, and zsh takes a word that begins with "=" for
    a command's path: "[ a == b ]", the usual bash test, and "echo ===="
    failed with "= not found". The line gnosh puts in front of a host
    command now turns that off in zsh (setopt no_equals) and is nothing
    to the other shells. As in Claude Code 2.1.328 (changelog read
    2026-11-19). Commands on this machine run in /bin/bash, as before.

199. **/review marks its findings as Claude Code 2.1.328's /code-review
    does.** It reviews for bugs and now also for what goes against the
    project's instruction files (GNOSH.md, CLAUDE.md, AGENTS.md); each
    finding is marked Important (the changes bring it), Pre-existing (it
    was there, in code the changes touch) or Nit, and ends with the fix
    suggested; a review that finds nothing says "Code review found no
    issues." (changelog read 2026-11-19). Reuse, simplification and
    efficiency stay /simplify's.

200. **The config review's quick fixes (2026-11-19).** A fresh-eyes review
    of the settings loader and the folder-trust gate:
    - A project's own file cannot set options.projectDataDir. The data
      folder is where the project's local settings are read from; the
      trust question was asked of .gnosh/settings.local.json while a Yes
      put in force the file of a folder the repository had named (with
      a hook and an allow rule in it). Only the person's own files, /config
      and --settings move it now, like the other options a repository must
      not change.
    - --settings naming the folder's own settings file is refused in an
      untrusted folder by what the file is, not by how its path is spelled:
      through a link (/tmp on a Mac) or in another case on a disk that
      ignores case, it was read as the command line's file.
    - A skill's name in skillOverrides is shown made safe in a message,
      as every other key is: the file is read before any question, and an
      escape in it reached the terminal.
    - A named pipe where a repository's git settings would be is refused
      before it is opened: gnosh hung at start in such a folder.

201. **The trust question shows everything a Yes turns on.** It listed
    what the folder's settings files hold and the programs its git
    settings name; a Yes also turned on the servers of the project's
    .mcp.json, its skills and commands (their hooks, the commands they
    run as they are used, the tools they let run without asking) and its
    kinds of sub-agent (their hooks, the MCP servers they start). A
    repository with one harmless option and a .mcp.json server that ran
    a stranger's command got a question that named only the option, and
    a folder with nothing else of its own was never asked at all (the
    config review of 2026-11-19). Those are listed now, with what each
    runs (a server's command as the file writes it, no variable filled
    in) or lets run, and counted: a folder that has any of them is asked
    about, once, when gnosh starts there, at /cd and by gnosh trust.

202. **The person's status line and file suggestion commands are held where
    git would run a repository's programs.** In a folder that is not
    trusted, whose repository's own git settings name a program (a
    file-system monitor, a filter, a diff driver), gnosh holds its own
    look at the repository, since git would run that program. The
    person's status line and fileSuggestion commands still ran there, and
    a status line that calls git status ran the repository's monitor (the
    config review of 2026-11-19). In such a folder the status line is not
    started, with a notice that says why, and the @ picker uses gnosh's
    own search, which keeps git from running them.

203. **A variable the person's env setting takes away is gone from every
    command gnosh starts.** "env": {"NAME": ""} takes NAME away; the Bash
    tool and hooks did that, while the status line, the file suggestion
    command, a skill's own commands (!`...`) and gnokey were handed the
    whole inherited environment with the settings appended, so the one
    taken away stayed (the config review of 2026-11-19). The person's own
    commands now get the inherited environment under the env block, a
    bare name taking a variable out; a skill's commands get the Bash
    tool's environment, which also leaves out what changes how a shell
    reads its line (BASH_ENV, SHELLOPTS, PS4, exported functions).

204. **The config review's last fixes (2026-11-19).**
    - An http hook's address is shown and logged without what can be its
      secret: the user name, the query, and each long piece of the path
      (a Slack or Discord webhook's token). It went whole to /status,
      gnosh doctor, /hooks and the hook records.
    - A project's env block cannot set the variables that move where
      gnosh and the programs it starts keep their own files (HOME, TMPDIR,
      XDG_*, CLAUDE_CONFIG_DIR, GNOSH_*_DIR), trusted or not, as
      spec/cc/settings.md 2 says; a trusted project's could.
    - disableBypassPermissionsMode only takes a mode away, so a folder
      that is not trusted keeps it too, as DECISIONS 2 says of what only
      restricts (a new kind of setting, "restriction"); it was held.

205. **The oracle no longer counts a redirected command against the
    engine.** Its check that every command the engine read did run
    trusted bash's trace, and a command whose standard error goes
    elsewhere takes its own trace line with it (seed 157's ":&> $((x))"
    ran and made the file 0, which then held the trace), while one whose
    redirection fails does not run at all. The check passes over a
    command with redirections; reading more than runs is the safe way
    round. Also: the config doc said a provider key in an env block wins
    over the shell's; it is read from the shell alone, as the code does.

206. **The file --settings names is watched where it was read.** The
    watcher made the path again from the flag, relative to the folder the
    session works in; the loader reads it relative to where gnosh was
    started, with a ~ filled in. With --cwd (or a quoted ~) it watched
    another file, and edits to the one in force were never taken in (the
    config review of 2026-11-19). It watches the loader's path now.

208. **gnosh does not rewrite a local settings file that holds a key
    twice.** Saving an approval reads the project's local file, changes its
    rules and writes it back; it read the file as Go does, keeping the last
    of a key given twice, so a duplicate the person had added (which the
    loader refuses, with a mistake) lost its first copy unseen at the next
    save (the config review of 2026-11-19). The file is left as it is now,
    and the message names the key (config.DuplicateKey).

209. **The agent loop review's fixes (2026-11-19).** A fresh-eyes review of
    internal/agent:
    - Stopping a turn drops only the prompts gnosh queued for itself
      (Prompt.Own: /loop's next prompt, the "go on" after the session
      moved, and notes that were to start a turn, which stay as notes).
      It dropped every hidden prompt, among them the person's own: the
      output of a !command they ran while a turn worked, a skill, /review.
      Esc lost that output, and nobody was told.
    - A stop as a round's tools end ends the turn as stopped at once, and
      the fold of what the person sent meanwhile checks for a stop under
      the lock Interrupt cancels under. A prompt in line was folded into
      the stopped turn and never answered when Esc came while another
      prompt was still in its check.
    - /loop goes off when its prompt is refused (a session start that said
      stop): it sent the next one at once, refused the same way, until
      Esc.

210. **Esc and End stop the moments that run after a turn ended.** The
    turn-failed and turn-ended moments (StopFailure and Stop hooks) ran
    under the session's own context, which only End cancelled, after it
    had waited for them: a hook that hung there (ten minutes by default)
    held the next prompt, Esc did nothing, and /exit waited (the agent
    loop review of 2026-11-19). They run under a context of their own now,
    which Interrupt and End cancel first.

211. **The oracle's redirect exception keeps the checks that end a line.**
    DECISIONS 205 passed over a redirected command before the checks that
    end the whole line, so seed 158 counted against the engine a command
    after a redirected exec (a failed exec ends the shell) and one after a
    redirected command holding ${} (a bad substitution ends a
    non-interactive bash). The redirected command is now left out of what
    must have run only after those checks, and a redirection whose word
    the engine cannot know (> ${}, &> $((x))) ends the check for the line.
    Probed in fresh jails: all three lines of seeds 157 and 158 pass.

212. **A prompt in line is not returned after a turn's end failed to be
    written.** When a turn's end cannot be written (a full disk), the next
    turn settles the log first, and that pass took every prompt written
    down as queued and not sent for one a dead gnosh left behind: the
    person was told it was not sent while it went out all the same, twice
    once they sent it again (the agent loop review of 2026-11-19). In a
    session this process opened, such prompts are still in its line, so
    the pass runs only when a session is opened.

213. **A prompt taken back during its check is handed back once.** One
    taken back (Up, /withdraw) while its UserPromptSubmit check still ran
    stayed in intake for the admitting goroutine to drop, marked taken,
    and nothing passed over the mark: a second take handed it back again,
    so the box held its text twice, and it was counted and shown as
    waiting until its check ended (the agent loop review of 2026-11-19).
    Taken prompts are passed over now, and the queue is told again when
    the last one is dropped.

214. **End writes off the prompts it returns.** A prompt written down as
    queued and handed back by End (the session closed, or /clear opened a
    new one) had no record that it was handed back, so the session opened
    again returned it a second time (the agent loop review of
    2026-11-19). End writes it off as it returns it.

215. **A note stopped in its check is kept.** A note that was to start a
    turn (a background task's end, a channel's event) passes the session's
    start like a prompt; stopped there (Esc while the SessionStart hook
    ran for it), it came back as a hidden prompt that no front end shows,
    and was lost, against Tell's promise that stopping never loses a note
    (the agent loop review of 2026-11-19). It is kept as a note now, and
    goes with the next request.

216. **With two questions open, the session says it waits on the one
    left.** Parallel sub-agents can each ask at once. The answer to the
    first put back the phase from before it, so the session read as
    working while the second question still waited, and the second's end
    put back the first's "waiting for an answer" (the agent loop review of
    2026-11-19). What the session did before the first question is put
    back when the last one closes; until then it waits on one still open.
