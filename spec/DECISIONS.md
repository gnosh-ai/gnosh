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
    workspace.git_worktree. Not built: .worktreeinclude, a pull
    request's head (#123), and worktree.baseRef.
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
