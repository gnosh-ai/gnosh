package memjoin

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/gnovm"
	gnovmtools "github.com/gnosh-ai/gnosh/internal/gnovm/tools"
)

// Options says how the join reaches the GnoVM. The zero value is what the
// program itself uses: the machines run in a worker, which is this same
// program started with the one word "gnovm-worker".
type Options struct {
	// InProcess runs the GnoVM machines and mem inside this process and
	// starts no worker. Tests set it, always.
	InProcess bool

	// Binary names the worker's program. Empty means this program itself.
	// Under go test the carried client refuses to start a worker at all
	// unless a binary is named here, and it must not be the test binary.
	Binary string

	// watch, when not nil, is shown every request the tools send to the
	// GnoVM, before it is sent. It is for the package's own tests.
	watch func(req gnovm.RunRequest)
	// joined, when not nil, is shown each session's contribution with the
	// join behind it. It is for the package's own tests too.
	joined func(c *app.Contribution, j *join)
}

// The data directory's name under gnosh's own data directory, and the name
// of the one option of the config that is read at program start.
const dataDirName = "gnovm"

// DataDir is where the GnoVM machines and every project's mem are kept:
// the folder gnovm under gnosh's own data directory (state), which is
// $XDG_DATA_HOME/gnosh, or ~/.local/share/gnosh. It is outside every
// project on purpose: a cloned repository cannot bring machines or a mem
// with it.
func DataDir(state string) string {
	return filepath.Join(state, dataDirName)
}

// process is the join's hold on what one run of the program shares: the
// one client of the GnoVM, and the sessions open in this process.
type process struct {
	opts Options

	mu    sync.Mutex
	vms   *gnovm.Lazy
	dir   string
	joins map[string]*join
}

// New returns the feature that makes mem and frames work in the program.
// It is handed to app.Options.Features; nothing in it is started until a
// session first uses a machine or mem.
func New(o Options) app.Feature {
	p := &process{opts: o, joins: map[string]*join{}}
	return app.Feature{
		Name: "mem",
		Tools: []app.ToolName{
			// The four run code in sandboxed machines and touch nothing
			// but what this feature keeps, so none of them asks. Deleting
			// a machine is the one thing a person must be asked about, and
			// the tool says so itself (core.Action.Confirm).
			//
			// gnovm_run is the one tool mem's text passes through, in both
			// directions: the code of a call holds what the model keeps,
			// and its answer what mem holds. An erasure takes a text out
			// of mem and out of the sessions' files (erase.go); it cannot
			// reach gnosh's log. So the arguments and results of this tool
			// are kept out of the log and out of debugging output. The
			// other three name machines and never carry mem's text, and
			// stay in the log as they are.
			{Name: gnovmtools.RunToolName, Quiet: true, NoLogArgs: true},
			{Name: gnovmtools.NewToolName, Quiet: true},
			{Name: gnovmtools.ListToolName, Quiet: true},
			{Name: gnovmtools.DropToolName, Quiet: true},
		},
		Built: []string{string(cli.CmdMem), string(cli.CmdBridge), "--goal"},
		Commands: []app.Command{
			{Command: cli.CmdMem, Run: p.memCommand},
			{Command: cli.CmdBridge, Run: p.bridgeCommand},
		},
		Session:  p.session,
		Shutdown: p.shutdown,
	}
}

// client returns the process's hold on the GnoVM, made the first time a
// session or a command needs it. Making it starts nothing: the client
// itself, and with it a worker, comes only with the first call. One
// process has one client, so the directory the first caller names stands
// for the process.
func (p *process) client(dir, embeddings string) *gnovm.Lazy {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.vms == nil {
		p.dir = dir
		p.vms = gnovm.NewLazy(gnovm.ClientOptions{
			Options:   gnovm.Options{Dir: dir, Embeddings: embeddings},
			InProcess: p.opts.InProcess,
			Binary:    p.opts.Binary,
		})
	}
	return p.vms
}

// shutdown lets go of the client as the program ends. The wiring calls it
// after the last session's file is flushed and released, which is the
// order the client's own design asks for.
func (p *process) shutdown(context.Context) {
	p.mu.Lock()
	vms := p.vms
	p.vms = nil
	p.mu.Unlock()
	if vms != nil {
		vms.Close()
	}
}

// add and remove keep the list of the sessions open in this process, so
// that an erasure can reach the one that holds a session's file open.
func (p *process) add(j *join) {
	p.mu.Lock()
	p.joins[j.id] = j
	p.mu.Unlock()
}

func (p *process) remove(j *join) {
	p.mu.Lock()
	if p.joins[j.id] == j {
		delete(p.joins, j.id)
	}
	p.mu.Unlock()
}

// open returns the join of a session that is open in this process, or nil.
func (p *process) open(id string) *join {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.joins[id]
}
