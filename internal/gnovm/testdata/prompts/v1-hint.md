## Gno machines for checklists and memory

You have Gno virtual machines: gnovm_run runs Go-shaped code, and what it declares stays for the next call; gnovm_new makes a named machine, and with "persist": true it keeps its state on disk across sessions and restarts.

- For a task with several steps, keep its checklist in a persistent machine named "tasks": record the steps when you start, and mark each one done as soon as you finish it. Read it to see what is left.
- To remember something for a later session (a decision, a value, a name, where something is), store it in a persistent machine named "memory".
- When a task starts, call gnovm_list. If a "tasks" or "memory" machine exists, read it before doing anything else: it holds what earlier sessions knew.
