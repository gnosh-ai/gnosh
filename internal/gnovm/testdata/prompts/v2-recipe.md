## Gno machines for checklists and memory

You have Gno virtual machines: gnovm_run runs Go-shaped code, and what it declares stays for the next call; gnovm_new makes a named machine, and with "persist": true it keeps its state on disk across sessions and restarts. Use them as your checklist and your memory, exactly like this.

**When a task starts**, call gnovm_list. If a "tasks" machine exists, run `println(Show())` in it; if a "memory" machine exists, run `Recall()` in it. Do this before anything else: they hold what earlier sessions knew and did.

**Checklist.** For a task with more than one step, make the checklist once:

1. gnovm_new {"name": "tasks", "persist": true, "description": "task checklist"}
2. gnovm_run {"name": "tasks", "code": "import \"strconv\"\nimport \"strings\"\n\ntype Task struct {\n\tTitle string\n\tDone  bool\n}\n\nvar Tasks []Task\n\nfunc Add(title string) { Tasks = append(Tasks, Task{Title: title}) }\n\nfunc Done(i int) { Tasks[i].Done = true }\n\nfunc Show() string {\n\tvar b strings.Builder\n\tfor i, t := range Tasks {\n\t\tmark := \" \"\n\t\tif t.Done {\n\t\t\tmark = \"x\"\n\t\t}\n\t\tb.WriteString(\"[\" + mark + \"] \" + strconv.Itoa(i) + \" \" + t.Title + \"\\n\")\n\t}\n\treturn b.String()\n}"}

Then add each step with gnovm_run {"name": "tasks", "code": "Add(\"write the parser\")"}, and the moment a step is finished, mark it: gnovm_run {"name": "tasks", "code": "Done(0)\nprintln(Show())"}. If the "tasks" machine already exists, do not declare these again: just call Add, Done and println(Show()).

**Memory.** To remember a fact for later sessions:

1. Once: gnovm_new {"name": "memory", "persist": true, "description": "facts to remember"}, then gnovm_run {"name": "memory", "code": "var Notes = map[string]string{}\n\nfunc Remember(key, value string) { Notes[key] = value }\n\nfunc Recall() map[string]string { return Notes }"}
2. Each fact: gnovm_run {"name": "memory", "code": "Remember(\"staging database\", \"db-1\")"}

Keep checklist titles and memory values short and specific.
