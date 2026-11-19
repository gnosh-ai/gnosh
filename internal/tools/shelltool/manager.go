package shelltool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Manager runs the shell commands of one session. It knows every process
// it has started, so that none is left behind: Close stops them all.
// Make one with New. Its methods and its tools may be used from several
// goroutines at once.
type Manager struct {
	opt   Options // with every default filled in
	shell string  // the shell that runs commands

	// The machines a call may name as its host, and this one as results
	// write it. hosts and hostNames are put in place whole (SetHosts) and
	// never changed in place: a reader takes both under hostsMu.
	hostsMu   sync.RWMutex
	hosts     map[string]*Host // the declared hosts by name
	hostNames []string         // their names, in the order declared
	local     string           // who and where this machine is: "dev@box"

	// The ways the manager touches other processes. They are fields so
	// that a test can stand in for them.
	signal func(pid int, sig syscall.Signal) error
	procs  func(sid int) ([]procInfo, error)
	envHas func(pid int, entry string) (bool, error)

	// mark is the value of MarkVar in every command's environment, which
	// the processes they start inherit: one that carries it is this
	// manager's to stop, wherever its session's id has gone since.
	mark string

	notes *notifier // tells Options.OnEnd about background commands that ended

	// maxLingering is how many ended commands may have a process they left
	// behind listened to at once. Each costs a goroutine and two open
	// files, so there has to be an end to it.
	maxLingering int

	// pumpDelay holds back the reading of every command's output. It is
	// zero outside tests, which use it to stand for a very busy machine.
	pumpDelay time.Duration

	mu        sync.Mutex
	closed    bool
	dir       string              // the manager's own directory for output files; made when first needed
	seq       int                 // how many commands have been let in
	bgSeq     int                 // how many background ids have been given out
	bgRunning int                 // how many background commands have not ended
	running   map[*job]struct{}   // commands whose end is not final yet
	lingering map[*job][]procInfo // ended commands whose output is still held open, with the processes they left
	detached  map[int][]procInfo  // sessions of ended commands where something that let go of their output runs on, with those processes
	tasks     map[string]*job     // background commands by id; ended ones stay
	order     []string            // the ids in tasks, oldest first
	// ended are the ids of the background commands that have ended, in
	// the order they ended: maxEndedTasks of them are kept.
	ended     []string
	calls     map[string]*job // foreground commands by the id of the call that waits for them
	pending   map[string]bool // calls about to start a foreground command, by id: true once asked to go to the background as it starts
	kept      []keptFile      // output files of finished foreground commands, oldest first
	keptBytes int64           // how much disk those files take

	wg sync.WaitGroup // every goroutine the manager has started

	closeOnce sync.Once
	closeErr  error
}

// MarkVar is the variable every command is given, set to a value of its
// session's own: what a command leaves running is known by it when the
// session ends (stop.go, sweep).
const MarkVar = "GNOSH_SHELL_ID"

// newMark makes a manager's mark: 16 random bytes, in hex.
func newMark() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// markEntry is the manager's mark as an entry of an environment.
func (m *Manager) markEntry() string { return MarkVar + "=" + m.mark }

// marked keeps the processes of ps whose environment holds the manager's
// mark: what a command of this session started, and what that started in
// turn, unless one began its program with an environment of its own.
func (m *Manager) marked(ps []procInfo) []procInfo {
	var out []procInfo
	for _, p := range ps {
		if ok, err := m.envHas(p.pid, m.markEntry()); err == nil && ok {
			out = append(out, p)
		}
	}
	return out
}

// keptFile is the output file of a finished foreground command.
type keptFile struct {
	path string
	size int64
}

// errClosed is returned for anything asked of a closed manager.
var errClosed = errors.New("shelltool: the manager is closed")

// refusal is a failure the model can do something about. It reaches the
// model as the text of a failed tool result; any other error from starting
// a command is the harness's own trouble and goes back as a Go error.
type refusal string

func (r refusal) Error() string { return string(r) }

// New makes a manager. It starts nothing and touches no files until the
// first command runs.
func New(opts Options) *Manager {
	m := &Manager{
		opt:    opts.withDefaults(),
		signal: syscall.Kill,
		procs:  sessionProcs,
		envHas: procEnvHas,
		mark:   newMark(),

		maxLingering: defaultMaxLingering,
		running:      make(map[*job]struct{}),
		lingering:    make(map[*job][]procInfo),
		detached:     make(map[int][]procInfo),
		tasks:        make(map[string]*job),
		calls:        make(map[string]*job),
		pending:      make(map[string]bool),
	}
	m.shell = m.opt.Shell
	if m.shell == "" {
		m.shell = "/bin/sh"
		if st, err := os.Stat("/bin/bash"); err == nil && st.Mode().IsRegular() {
			m.shell = "/bin/bash"
		}
	}
	m.local = m.opt.Local
	if m.local == "" {
		m.local = whoAndWhere()
	}
	m.hosts, m.hostNames = hostTable(m.opt.Hosts)
	m.notes = newNotifier(m.opt.OnEnd, &m.wg)
	return m
}

// hostTable makes the table of declared hosts. The name of this machine is
// never a host's, and a name stands for one machine: the config refuses
// both, and here the first declaration of a name is the one that counts.
func hostTable(list []Host) (map[string]*Host, []string) {
	hosts := make(map[string]*Host, len(list))
	var names []string
	for _, h := range list {
		if _, twice := hosts[h.Name]; h.Name == "" || h.Name == LocalName || twice {
			continue
		}
		hosts[h.Name] = &h
		names = append(names, h.Name)
	}
	return hosts, names
}

// SetHosts puts hosts in place of the machines calls may name, while the
// manager runs (the settings that declare them changed); a command already
// running goes on where it is.
func (m *Manager) SetHosts(hosts []Host) {
	table, names := hostTable(hosts)
	m.hostsMu.Lock()
	m.hosts, m.hostNames = table, names
	m.hostsMu.Unlock()
}

// HostNames are the names of the machines calls may name, in the order
// declared.
func (m *Manager) HostNames() []string {
	_, names := m.hostSet()
	return slices.Clone(names)
}

// hostSet is the table of declared hosts as it stands.
func (m *Manager) hostSet() (map[string]*Host, []string) {
	m.hostsMu.RLock()
	defer m.hostsMu.RUnlock()
	return m.hosts, m.hostNames
}

// PersonsBash returns the Bash tool for a command the person types into the
// session themselves. It runs the command exactly as Bash does, with the
// same shell, limits and line at the head of the result, and without the
// rules and notes that are there to steer a model: a person may type ssh.
func (m *Manager) PersonsBash() core.Tool {
	return bashTool{m: m, person: true}
}

// Tools returns the tools the manager offers a model: Bash, TaskOutput
// (unless Options.NoOutputTool) and TaskStop. Each is also a
// core.Describer.
func (m *Manager) Tools() []core.Tool {
	tools := []core.Tool{bashTool{m: m}}
	if !m.opt.NoOutputTool {
		tools = append(tools, outputTool{m})
	}
	tools = append(tools, stopTool{m})
	if m.opt.OnMonitor != nil && !m.opt.NoBackground {
		// Monitor is there when there is somewhere to pass its lines.
		tools = append(tools, monitorTool{m})
	}
	return tools
}

// Tasks lists the session's background commands, oldest first, ended ones
// included.
func (m *Manager) Tasks() []Task {
	m.mu.Lock()
	jobs := make([]*job, 0, len(m.order))
	for _, id := range m.order {
		jobs = append(jobs, m.tasks[id])
	}
	m.mu.Unlock()
	now := m.opt.Now()
	out := make([]Task, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.task(now))
	}
	return out
}

// Stop stops the background command with the given id, as the TaskStop
// tool does, and returns it as it then is. It is for the person's own
// controls. Stopping a command that has already ended is not an error.
func (m *Manager) Stop(ctx context.Context, id string) (Task, error) {
	j := m.find(id)
	if j == nil {
		return Task{}, fmt.Errorf("shelltool: there is no background command %q", id)
	}
	if err := j.stopAndWait(ctx); err != nil {
		return j.task(m.opt.Now()), err
	}
	return j.task(m.opt.Now()), nil
}

// Background moves the foreground command that the tool call callID is
// waiting for to the background, where it keeps running; the call returns
// at once with the command's task id. It is for the person's own controls.
// It reports whether there was such a command to ask. A call whose command
// is about to start goes to the background as it starts: the person sees
// the call before its command is running, and asking then is not asking
// too early. A command on another machine is not one: it runs for as long
// as its call holds the connection, and stays in the foreground.
func (m *Manager) Background(callID string) bool {
	m.mu.Lock()
	j := m.calls[callID]
	if _, waiting := m.pending[callID]; j == nil && waiting && !m.opt.NoBackground {
		m.pending[callID] = true
		m.mu.Unlock()
		return true
	}
	m.mu.Unlock()
	if j == nil || j.host != nil || m.opt.NoBackground {
		return false
	}
	select {
	case j.move <- struct{}{}:
	default:
	}
	return true
}

// BackgroundSoon is Background for a call the caller knows is a call of
// this manager's Bash tool in flight, which may not have reached the
// manager yet: the person sees it while its permission is checked and its
// PreToolUse hooks run. Its command goes to the background as it starts,
// and one that runs already goes now. A request for a call that never
// runs (refused, or stopped first) stays on the books until the session
// ends: a few bytes.
func (m *Manager) BackgroundSoon(callID string) bool {
	m.mu.Lock()
	if j := m.calls[callID]; j == nil {
		ok := callID != "" && !m.opt.NoBackground && !m.closed
		if ok {
			m.pending[callID] = true
		}
		m.mu.Unlock()
		return ok
	}
	m.mu.Unlock()
	return m.Background(callID)
}

// expect notes that the call callID is about to start a foreground
// command here, so that Background can be asked for it before it starts;
// the function it returns forgets the call. (A request made before expect,
// by BackgroundSoon, is kept.)
func (m *Manager) expect(callID string) (forget func()) {
	m.mu.Lock()
	if _, ok := m.pending[callID]; !ok {
		m.pending[callID] = false
	}
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.pending, callID)
		m.mu.Unlock()
	}
}

// LeftRunning says what Close would stop of what finished commands left
// running, holding their output or not (cmd >log 2>&1 &, nohup cmd &):
// each process group still there, with its programs, as the command's
// result named it ("process group 4242 (python3)"). What started a
// session of its own (setsid) is not in it: Close leaves that alone. It is
// what a run tells the model before it ends (app's printwait.go), so that
// a server the task needs is not stopped unseen.
func (m *Manager) LeftRunning() []string {
	m.mu.Lock()
	sessions := make(map[int][]procInfo, len(m.lingering)+len(m.detached))
	for j, procs := range m.lingering {
		sessions[j.pid] = append(sessions[j.pid], procs...)
	}
	for sid, procs := range m.detached {
		sessions[sid] = append(sessions[sid], procs...)
	}
	m.mu.Unlock()
	var out []string
	for sid, then := range sessions {
		if sid <= 1 || len(then) == 0 {
			continue
		}
		// As sweep finds them: the same processes, or else those that
		// carry the session's mark.
		now, err := m.procs(sid)
		if err != nil || len(now) == 0 {
			continue
		}
		if !sameProcess(now, then) {
			if now = m.marked(now); len(now) == 0 {
				continue
			}
		}
		if text, _ := describeGroups(now); text != "" {
			out = append(out, text)
		}
	}
	slices.Sort(out)
	return out
}

// StopLeftRunning stops what finished commands left running, as Close
// does at the end, and returns what LeftRunning said of it: a run about to
// end stops them before it tells the model, so that one started again to
// outlive the run (with setsid) finds its port and its files free. The
// manager goes on.
func (m *Manager) StopLeftRunning() []string {
	left := m.LeftRunning()
	m.stopLeft(false)
	return left
}

// stopLeft stops what finished commands left behind, still holding their
// output, and what they left that let go of it; closing, it also closes
// their output and waits for what was read of it.
func (m *Manager) stopLeft(closing bool) {
	m.mu.Lock()
	left := make(map[*job][]procInfo, len(m.lingering))
	for j, procs := range m.lingering {
		left[j] = procs
	}
	detached := m.detached
	m.detached = make(map[int][]procInfo)
	m.mu.Unlock()
	var sweeps sync.WaitGroup
	for j, procs := range left {
		sweeps.Go(func() {
			m.sweep(j.pid, procs)
			if closing {
				j.pipe.Close()
				waitFor(j.pumped, m.opt.PipeGrace+time.Second)
			}
		})
	}
	for sid, procs := range detached {
		sweeps.Go(func() { m.sweep(sid, procs) })
	}
	sweeps.Wait()
}

// Close stops every command the manager started and is still running,
// foreground and background, with everything those commands started; kills
// what a finished command left behind if that still holds the command's
// output open; waits for all of it to be gone; and removes the output
// files. Tool calls in flight return, saying that their command was
// stopped. After Close the manager runs nothing more. Close may be called
// more than once. Its error says what could not be stopped or removed.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() { m.closeErr = m.close() })
	return m.closeErr
}

func (m *Manager) close() error {
	m.mu.Lock()
	m.closed = true
	running := make([]*job, 0, len(m.running))
	for j := range m.running {
		running = append(running, j)
	}
	m.mu.Unlock()

	// Every command is stopped at the same time; each takes at most its
	// graces. The wait below is longer than any of them can take, and is
	// there so that even a fault here cannot make Close hang.
	for _, j := range running {
		j.ask(endClosed)
	}
	var problems []string
	patience := time.NewTimer(m.opt.TermGrace + m.opt.KillWait + 2*m.opt.PipeGrace + drainWait + 5*time.Second)
	defer patience.Stop()
	late, expired := 0, false
	for _, j := range running {
		if !expired {
			select {
			case <-j.done:
				continue
			case <-patience.C:
				expired = true
			}
		}
		select {
		case <-j.done:
		default:
			late++
		}
	}
	if late > 0 {
		problems = append(problems, fmt.Sprintf("%s did not stop in time", plural(int64(late), "command")))
	}
	for _, j := range running {
		if e := j.ending(); e != nil && len(e.alive) > 0 {
			problems = append(problems, fmt.Sprintf("%s could not be stopped: %s", j.label(), describeProcs(e.alive)))
		}
	}

	m.stopLeft(true)
	m.notes.close()

	// Every goroutine should be done by now. One that is not is waiting on
	// a process that would not die, or on a callback that blocks.
	idle := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(idle)
	}()
	if !waitFor(idle, 2*time.Second) {
		problems = append(problems, "some of the manager's goroutines are still waiting on a process or a callback")
	}

	m.mu.Lock()
	dir := m.dir
	m.mu.Unlock()
	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			problems = append(problems, fmt.Sprintf("removing the output files in %s: %v", dir, err))
		}
	}
	if len(problems) > 0 {
		return errors.New("shelltool: closing: " + strings.Join(problems, "; "))
	}
	return nil
}

// spoolDir returns the manager's directory for output files, making it if
// it is not there.
func (m *Manager) spoolDir() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errClosed
	}
	if m.dir != "" {
		// A cleaner of temporary files may have taken it.
		if err := os.MkdirAll(m.dir, 0o700); err != nil {
			return "", fmt.Errorf("shelltool: restoring the directory for output files: %w", err)
		}
		return m.dir, nil
	}
	parent := m.opt.SpoolDir
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("shelltool: making the directory for output files: %w", err)
	}
	dir, err := os.MkdirTemp(parent, "gnosh-shell-")
	if err != nil {
		return "", fmt.Errorf("shelltool: making the directory for output files: %w", err)
	}
	m.dir = dir
	return dir, nil
}

// admit lets a new command in: it names its files, gives a background
// command its id, and puts it among the running ones, so that Close will
// find it from this moment on. callID is the tool call that will wait for
// a foreground command. A background command is not yet listed among the
// tasks: it has not started, and nothing may ask about it until it has
// (see publish).
func (m *Manager) admit(j *job, background bool, callID string) (name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errClosed
	}
	if background && m.opt.NoBackground {
		return "", refusal("Background commands are off in this session. Run the command in the foreground, with a timeout that leaves it room.")
	}
	if background && m.bgRunning >= m.opt.MaxBackground {
		return "", refusal(fmt.Sprintf("%d background commands are already running, which is the most allowed at once. Stop one with %s first, or run this command in the foreground.", m.bgRunning, StopName))
	}
	m.seq++
	name = fmt.Sprintf("cmd-%d", m.seq)
	if background {
		name = m.nextTaskID(j)
	} else if callID != "" {
		j.callID = callID
		m.calls[callID] = j
		if m.pending[callID] && j.host == nil {
			// Asked to go to the background before it started.
			select {
			case j.move <- struct{}{}:
			default:
			}
		}
	}
	m.running[j] = struct{}{}
	return name, nil
}

// nextTaskID makes j a background command by giving it the next id, which
// it returns. It must be called with mu held.
func (m *Manager) nextTaskID(j *job) string {
	m.bgSeq++
	m.bgRunning++
	id := fmt.Sprintf("bg%d", m.bgSeq)
	j.setTaskID(id)
	return id
}

// publish lists a background command that has started among the tasks,
// where the tools and Tasks find it by its id.
func (m *Manager) publish(j *job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list(j)
}

// list adds a started background command to the tasks. It must be called
// with mu held.
func (m *Manager) list(j *job) {
	id := j.taskID()
	m.tasks[id] = j
	m.order = append(m.order, id)
}

// adopt turns a running foreground command into a background one. It
// reports false, and changes nothing, when the manager is closed or already
// has as many background commands as it may.
func (m *Manager) adopt(j *job) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.bgRunning >= m.opt.MaxBackground {
		return false
	}
	delete(m.calls, j.callID)
	m.nextTaskID(j)
	m.list(j)
	return true
}

// unlist forgets which call waits for a foreground command, once that call
// is about to return.
func (m *Manager) unlist(j *job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls[j.callID] == j {
		delete(m.calls, j.callID)
	}
}

// linger records that an ended command left processes behind, left, that
// still hold its output open, so that Close can deal with them. The command
// stays on that list until its output closes (see settle). It reports false
// when the manager is already closing, or is listening to as many such
// commands as it will; the caller then deals with this one at once.
//
// When the output closed in the meantime, settle has come and gone and
// found nothing on the list: what is still in the command's session let
// go of the output, and goes on the books of what did (detached), as settle
// would have put it.
func (m *Manager) linger(j *job, left []procInfo) bool {
	m.mu.Lock()
	if m.closed || len(m.lingering) >= m.maxLingering {
		m.mu.Unlock()
		return false
	}
	if !closed(j.pumped) {
		m.lingering[j] = left
		m.mu.Unlock()
		return true
	}
	m.mu.Unlock()
	if now, err := m.procs(j.pid); err == nil && len(now) > 0 {
		j.letGo.Store(true)
		if !m.keepDetached(j.pid, now) {
			m.sweep(j.pid, now) // (the manager is closing)
		}
	}
	return true
}

// keepDetached records that the ended command whose session is sid left
// processes running there that let go of its output, so that Close stops
// them. Sessions on the books whose processes have all ended are forgotten
// first. It reports false when the manager is already closing.
func (m *Manager) keepDetached(sid int, left []procInfo) bool {
	m.pruneDetached()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.detached[sid] = left
	return true
}

// pruneDetached forgets the sessions on the books (detached) in which no
// process is left. The sessions are looked at without the lock held; one
// put on the books again meanwhile, under the same id, is kept. A session
// must look empty twice, pruneRecheck apart: a look at a session is not one
// moment, and a process that started a last one and ended while it was
// looked at can make it look empty. (A clean-copy CI saw it: a launcher's
// shell started its server and ended as the session was looked at, the
// session was forgotten, and Close left the server running.)
func (m *Manager) pruneDetached() {
	m.mu.Lock()
	books := make(map[int][]procInfo, len(m.detached))
	for sid, procs := range m.detached {
		books[sid] = procs
	}
	m.mu.Unlock()
	empty := make(map[int][]procInfo)
	for sid, then := range books {
		if now, err := m.procs(sid); err != nil || len(now) > 0 {
			continue
		}
		empty[sid] = then
	}
	if len(empty) == 0 {
		return
	}
	time.Sleep(pruneRecheck)
	for sid, then := range empty {
		if now, err := m.procs(sid); err != nil || len(now) > 0 {
			continue
		}
		m.mu.Lock()
		if cur, ok := m.detached[sid]; ok && len(cur) > 0 && len(then) > 0 && cur[0] == then[0] {
			delete(m.detached, sid)
		}
		m.mu.Unlock()
	}
}

// stillThere looks at a session again, pruneRecheck later, and keeps of
// left the processes that are still in it. A look at a session is not one
// moment: a process that closed the output as it ended may be seen before
// it is gone (a CI run on a busy machine saw `(sleep 0.1; echo late) &`
// reported as left running).
func (m *Manager) stillThere(sid int, left []procInfo) []procInfo {
	time.Sleep(pruneRecheck)
	again, err := m.procs(sid)
	if err != nil {
		return left
	}
	var out []procInfo
	for _, p := range left {
		if slices.ContainsFunc(again, func(q procInfo) bool { return q.pid == p.pid && q.start == p.start }) {
			out = append(out, p)
		}
	}
	return out
}

// pruneRecheck is how long pruneDetached waits before it looks again at a
// session that looked empty.
const pruneRecheck = 50 * time.Millisecond

// settle is called when a command's output has closed for good. A command
// whose leftovers held it open (lingering) is taken off that list; when
// processes are still in its session, what held the output let go of it
// and runs on, and it goes on the books as what let go of the output as
// the command ended does (detached), so that Close stops it all the same.
// It goes on those books in the same moment it leaves the list, so that a
// Close at any point finds it on one of them. Once the manager is closing
// it stays on the list: close looks at the list only after it is marked
// closed, so the command is in what it looked at, or in what it will.
func (m *Manager) settle(j *job) {
	m.mu.Lock()
	_, held := m.lingering[j]
	closing := m.closed
	m.mu.Unlock()
	if !held || closing {
		return
	}
	now, err := m.procs(j.pid)
	left := err == nil && len(now) > 0
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	delete(m.lingering, j)
	if left {
		m.detached[j.pid] = now
		j.letGo.Store(true)
	}
	m.mu.Unlock()
	if left {
		m.pruneDetached()
	}
}

// maxEndedTasks is how many ended background commands are kept, each with
// its output's start and end in memory and its output file on disk.
var maxEndedTasks = 100

// release takes a command that has ended off the list of running ones. It
// is called once for each command that started, its ending recorded.
//
// A long session (a /loop, say) runs many background commands: of those
// that ended, the last maxEndedTasks are kept, and an older one is let go
// of, its output file with it unless something it left running still
// writes there. They all stayed until the session ended, in memory and on
// disk.
func (m *Manager) release(j *job) {
	var drop []*job
	m.mu.Lock()
	delete(m.running, j)
	if id := j.taskID(); id != "" {
		m.bgRunning--
		m.ended = append(m.ended, id)
		for len(m.ended) > maxEndedTasks {
			old := m.ended[0]
			m.ended = m.ended[1:]
			if oj := m.tasks[old]; oj != nil {
				delete(m.tasks, old)
				m.order = slices.DeleteFunc(m.order, func(x string) bool { return x == old })
				drop = append(drop, oj)
			}
		}
	}
	m.mu.Unlock()
	for _, oj := range drop {
		oj.mu.Lock()
		held := oj.end != nil && oj.end.held
		oj.mu.Unlock()
		if !held && oj.out != nil {
			os.Remove(oj.out.path)
		}
	}
}

// forget removes a command that never started.
func (m *Manager) forget(j *job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.running, j)
	if m.calls[j.callID] == j {
		delete(m.calls, j.callID)
	}
	if j.taskID() != "" {
		m.bgRunning--
	}
}

// find returns the background command with the given id, or nil.
func (m *Manager) find(id string) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[id]
}

// retain notes the output file of a finished foreground command as kept,
// and removes the oldest kept files while together they take more disk
// than the budget allows. The newest is never removed.
func (m *Manager) retain(path string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kept = append(m.kept, keptFile{path, size})
	m.keptBytes += size
	for m.opt.SpoolBudget >= 0 && m.keptBytes > m.opt.SpoolBudget && len(m.kept) > 1 {
		old := m.kept[0]
		m.kept = m.kept[1:]
		m.keptBytes -= old.size
		os.Remove(old.path)
	}
}
