package cli

import "fmt"

// Relaunch says how the session a command line opened stands now, for
// RelaunchArgs. An empty field leaves what the command line said.
type Relaunch struct {
	// Session is the session's id.
	Session string
	// Mode is the permission mode the session is in.
	Mode string
	// Model and Effort are the session's model and thinking level. They
	// take the place of -m and --effort where the command line gave those;
	// where it did not, the new start takes what the session saved.
	Model, Effort string
	// Tree is the name of the --worktree work tree the session is in.
	Tree string
}

// RelaunchArgs is the command line that starts gnosh again in the session
// args opened (/restart): every flag of args, written out whole
// (--name=value), but the ones that chose the conversation (-c, -r,
// --session-id, --fork-session, -n), which --resume=<session> replaces,
// and with the prompt words left out, since the session has sent them.
// The permission mode, the model and the thinking level are the session's
// as they are now, and a work tree whose name was made up is named.
func (t *Table) RelaunchArgs(args []string, r Relaunch) ([]string, error) {
	if r.Session == "" {
		return nil, fmt.Errorf("there is no session to resume")
	}
	p, err := t.walk(args)
	if err != nil {
		return nil, err
	}
	if p.help || p.cmd != t.root {
		return nil, fmt.Errorf("the command line did not open a session")
	}
	var out []string
	yolo, within := false, false
	for _, o := range p.occs {
		switch name := o.f.main(); {
		case o.f.id == idContinue || o.f.id == idResume || o.f.id == idSessionID || o.f.id == idFork || name == "--name":
			continue
		case o.f.id == idYolo:
			yolo = true
			continue
		case o.f.id == idMode && r.Mode != "":
			continue
		case name == "--allow-dangerously-skip-permissions":
			within = true
		case name == "--model" && r.Model != "":
			out = append(out, name+"="+r.Model)
			continue
		case name == "--reasoning-effort" && r.Effort != "":
			out = append(out, name+"="+r.Effort)
			continue
		case name == "--worktree" && r.Tree != "":
			out = append(out, name+"="+r.Tree)
			continue
		}
		out = append(out, o.spelled()...)
	}
	switch {
	case r.Mode == ModeBypassPermissions || r.Mode == "" && yolo:
		out = append(out, "--dangerously-skip-permissions")
	case r.Mode != "":
		out = append(out, "--permission-mode="+r.Mode)
		if yolo && !within {
			// Started skipping permissions, the session could go back to
			// it; the new start can too.
			out = append(out, "--allow-dangerously-skip-permissions")
		}
	}
	return append(out, "--resume="+r.Session), nil
}

// spelled is the flag as typed once more, written out whole: the main
// spelling, with its value after an equals sign, so that no value can be
// read as anything else. A list gives one flag for each argument it took.
func (o *occurrence) spelled() []string {
	name := o.f.main()
	switch o.f.kind {
	case kindText:
		return []string{name + "=" + o.value}
	case kindOptional, kindAttached:
		if o.has {
			return []string{name + "=" + o.value}
		}
	case kindList:
		// One flag for each argument that put something in the list: an
		// argument that put nothing ("," for rules, a blank path) would be
		// an empty list on its own, which is refused.
		var out []string
		for _, v := range o.raw {
			if items, _ := splitRules(v); o.f.list == listRules && len(items) == 0 || o.f.list != listRules && blank(v) {
				continue
			}
			out = append(out, name+"="+v)
		}
		if len(out) == 0 {
			// (A list with nothing in it parsed only where that means
			// something: --tools "".)
			out = []string{name + "="}
		}
		return out
	}
	return []string{name}
}
