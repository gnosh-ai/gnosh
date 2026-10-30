// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	"github.com/gnolang/gno/gnovm/stdlibs"
	"github.com/gnolang/gno/tm2/pkg/sdk"
	"github.com/gnolang/gno/tm2/pkg/store/cache"
)

// replPath is the package path every VM runs its code in. Each VM has its own
// store overlay, so VMs never see each other's package.
const replPath = "repl"

const (
	// maxOutput is how much printed output a call keeps.
	maxOutput = 64 << 10
	// maxValue is how long a rendered value may be.
	maxValue = 16 << 10
)

// vm is one resident Gno machine: an overlay on the base store with its own
// package, allocator, meter and output. Its state is exactly its journal's
// entries applied in order; after any failure that ran code, it is thrown
// away and rebuilt from the journal, which is the only correct reset (a
// panicking statement leaves the machine's stacks half run).
type vm struct {
	b     *base
	tx    gno.TransactionStore
	m     *gno.Machine
	pn    *gno.PackageNode
	alloc *gno.Allocator
	meter *callMeter
	out   capWriter

	capBytes int64
	// seq is the last journal entry applied, and offset where its line
	// ends in the journal (0 before any).
	seq    int64
	offset int64
	// imports maps each import name in the package to its path.
	imports map[string]string
	// methods holds "T.M" for every method declared, which the package
	// block's names do not include.
	methods map[string]bool
	// declBytes is the source size of the declarations the VM holds; their
	// syntax trees stay in memory (see budget.go). declFiles counts the
	// files they ran as: each makes later declarations slower to add.
	declBytes int64
	declFiles int
	// unusable is set once an operation that ran code failed.
	unusable bool
}

func newVM(b *base, capBytes int64) *vm {
	v := &vm{
		b:        b,
		meter:    &callMeter{limit: math.MaxInt64},
		capBytes: capBytes,
		imports:  map[string]string{},
		methods:  map[string]bool{},
	}
	v.out.max = maxOutput
	kv := cache.New(b.kv)
	v.tx = b.store.BeginTransaction(kv, kv, nil, v.meter)
	// Every import is in the base before the VM looks it up (base.ensure).
	v.tx.SetPackageGetter(nil)
	v.alloc = gno.NewAllocator(capBytes)
	v.pn = gno.NewPackageNode(replPath, replPath, &gno.FileSet{})
	pv := v.pn.NewPackage(v.alloc)
	// The package must be registered before the machine is made, or
	// NewMachineWithOptions loads it as an import.
	v.tx.SetBlockNode(v.pn)
	v.tx.SetCachePackage(pv)
	st := noEvictStore{v.tx}
	v.m = gno.NewMachineWithOptions(gno.MachineOptions{
		PkgPath:            replPath,
		Output:             &v.out,
		Store:              st,
		Alloc:              v.alloc,
		GasMeter:           v.meter,
		Context:            execContext(0, time.Unix(0, 0)),
		BoundedPanicRender: true,
	})
	v.m.SetActivePackage(pv)
	gno.Preprocess(st, v.pn, &gno.FileNode{FileName: "<repl>", PkgName: replPath})
	return v
}

// noEvictStore keeps the GnoVM's garbage collector from evicting the
// overlay's cached store objects. Those are standard library objects the GC
// walk does not mark (a VM's own package has no realm, so none of its objects
// are cached); evicting one that a VM value still references would make the
// next load decode a second copy of it.
type noEvictStore struct{ gno.TransactionStore }

func (noEvictStore) GarbageCollectObjectCache(int64) {}

// parsedOp is an op ready to run.
type parsedOp struct {
	op    op
	imp   gno.Decl
	file  *gno.FileNode
	stmts []gno.Stmt
	expr  gno.Expr
}

// parse parses every op of an entry. Nothing runs, so a parse error leaves
// the VM as it was. The meter must be armed: parsing is charged.
func (v *vm) parse(seq int64, ops []op) (parsed []parsedOp, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicError(r, v.meter)
		}
	}()
	for i, o := range ops {
		p := parsedOp{op: o}
		switch {
		case o.Import != nil:
			decls, err := v.m.ParseDecls(o.Import.source())
			if err != nil || len(decls) != 1 {
				return nil, fmt.Errorf("import %q: %v", o.Import.Path, err)
			}
			p.imp = decls[0]
		case o.File != "":
			name := fmt.Sprintf("in%d_%d.gno", seq, i)
			src := "package " + replPath + ";" + strings.Repeat("\n", max(o.Line-1, 0)) + o.File
			fn, err := v.m.ParseFile(name, src)
			if err != nil {
				return nil, &parseError{err}
			}
			p.file = fn
		case o.Stmts != "":
			stmts, err := v.m.ParseStmts(strings.Repeat("\n", max(o.Line-1, 0)) + o.Stmts)
			if err != nil {
				return nil, &parseError{err}
			}
			p.stmts = stmts
		case o.Call != "":
			stmts, err := v.m.ParseStmts(o.Call + "()")
			if err != nil {
				return nil, err
			}
			p.stmts = stmts
		case o.Eval != "":
			x, err := v.m.ParseExpr(o.Eval)
			if err != nil {
				return nil, &parseError{err}
			}
			if _, isCall := x.(*gno.CallExpr); !isCall {
				// Not gno's Eval wrapper: it binds its value to a
				// parameter named x, which hides a variable x.
				x, err = v.m.ParseExpr("func() any { return (" + o.Eval + ") }()")
				if err != nil {
					return nil, err
				}
			}
			p.expr = x
		default:
			return nil, errors.New("empty op")
		}
		parsed = append(parsed, p)
	}
	return parsed, nil
}

// checkNames refuses declarations of names the package already has, and
// imports that would rebind a name. The GnoVM keeps the first body of a
// redeclared function without a word, so a "fixed" function would go on
// running the old code.
func (v *vm) checkNames(parsed []parsedOp) error {
	have := map[string]bool{}
	for _, n := range v.pn.GetBlockNames() {
		have[string(n)] = true
	}
	for _, p := range parsed {
		switch {
		case p.op.Import != nil:
			name := p.op.Import.Name
			if path, ok := v.imports[name]; ok && path != p.op.Import.Path {
				return fmt.Errorf("%s already imports %q in this VM; import %q under another name, as in: import other %q",
					name, path, p.op.Import.Path, p.op.Import.Path)
			}
			if _, ok := v.imports[name]; !ok && have[name] {
				return redeclared(name)
			}
			have[name] = true
		case p.file != nil:
			for _, d := range p.file.Decls {
				for _, name := range declNames(d) {
					taken := have[name]
					if strings.Contains(name, ".") {
						taken = v.methods[name] || have[name]
					}
					if taken {
						return redeclared(name)
					}
					have[name] = true
				}
			}
		case p.stmts != nil:
			// A := earlier in the input declares names too.
			for _, s := range p.stmts {
				if as, ok := s.(*gno.AssignStmt); ok && as.Op == gno.DEFINE {
					for _, x := range as.Lhs {
						if nx, ok := x.(*gno.NameExpr); ok && nx.Name != "_" {
							have[string(nx.Name)] = true
						}
					}
				}
			}
		}
	}
	return nil
}

func redeclared(name string) error {
	return &refusal{fmt.Sprintf("%s is already declared in this VM. Assign to a variable with =, or use a new name; "+
		"to start over, pass reset: true (or use a fresh VM)", name)}
}

// declNames lists the names a top-level declaration declares, with methods
// as "T.M". Blank names and init functions declare nothing.
func declNames(d gno.Decl) []string {
	if fd, ok := d.(*gno.FuncDecl); ok {
		if fd.Name == "init" || fd.Name == "_" {
			return nil
		}
		if fd.IsMethod {
			return []string{receiverName(fd.Recv.Type) + "." + string(fd.Name)}
		}
		return []string{string(fd.Name)}
	}
	var out []string
	for _, n := range d.GetDeclNames() {
		if n != "_" {
			out = append(out, string(n))
		}
	}
	return out
}

// names returns the names the VM's package has.
func (v *vm) names() map[string]bool {
	have := map[string]bool{}
	for _, n := range v.pn.GetBlockNames() {
		have[string(n)] = true
	}
	return have
}

// shadowsBuiltin reports whether the package declares a name that
// normalize takes for a pure builtin, so an evaluation that calls it may
// change state after all.
func (v *vm) shadowsBuiltin() bool {
	for _, n := range v.pn.GetBlockNames() {
		if pureBuiltins[string(n)] {
			return true
		}
	}
	return false
}

func receiverName(x gno.Expr) string {
	if s, ok := x.(*gno.StarExpr); ok {
		x = s.X
	}
	if n, ok := x.(*gno.NameExpr); ok {
		return string(n.Name)
	}
	return x.String()
}

// run runs parsed ops in order, and returns the rendered value of an eval
// op if render is set. If it fails, the VM is unusable, with one exception:
// an input that is only an expression and fails to compile ran nothing (gno
// preprocesses an expression before it pushes anything to run it), so a
// typo does not cost a rebuild.
func (v *vm) run(parsed []parsedOp, render bool) (value string, err error) {
	evalOnly := len(parsed) == 1 && parsed[0].expr != nil
	defer func() {
		if r := recover(); r != nil {
			err = panicError(r, v.meter)
			var cerr *compileError
			if !evalOnly || !errors.As(err, &cerr) {
				v.unusable = true
			}
			if st := v.stacktrace(); st != "" {
				err = &runError{err: err, stack: st}
			}
		}
		if err == nil {
			// A trip the GnoVM recovered by itself still fails the call.
			if terr := v.meter.tripped(); terr != nil {
				v.unusable = true
				err = classifyTrip(terr)
			}
		}
	}()
	for _, p := range parsed {
		switch {
		case p.imp != nil:
			v.m.RunDeclaration(p.imp)
			v.imports[p.op.Import.Name] = p.op.Import.Path
		case p.file != nil:
			v.m.RunFiles(p.file)
			for _, d := range p.file.Decls {
				if fd, ok := d.(*gno.FuncDecl); ok && fd.IsMethod {
					v.methods[declNames(d)[0]] = true
				}
			}
			v.declFiles++
		case p.stmts != nil:
			for _, s := range p.stmts {
				v.m.RunStatement(gno.StageRun, s)
			}
		case p.expr != nil:
			tvs := v.m.Eval(p.expr)
			if render {
				value = renderValues(tvs)
			}
		}
		// Declarations and stored closures keep their syntax trees
		// (see vmCharge); other statements keep nothing.
		v.declBytes += p.op.declBytes()
	}
	return value, nil
}

// stacktrace renders the GnoVM's stack for an unhandled panic, bounded.
func (v *vm) stacktrace() (s string) {
	defer func() {
		if recover() != nil {
			s = ""
		}
	}()
	if v.m.Exception == nil {
		return ""
	}
	return truncate(v.m.Exception.Stacktrace.String(), 4<<10)
}

// apply parses and runs one journal entry, metered against gas and the
// deadline, with a fresh preprocess allocator. Replaying passes render
// false and a gas limit that never trips.
func (v *vm) apply(ctx context.Context, e *entry, gas int64, deadline time.Time, render bool) (value string, used int64, err error) {
	if v.unusable {
		return "", 0, errors.New("VM needs a rebuild")
	}
	v.out.reset()
	v.m.Context = execContext(e.Seq, time.Unix(0, e.Time))
	v.meter.begin(ctx, gas, deadline)
	defer v.meter.end()
	// The preprocess allocator never collects, so it is fresh per entry; it
	// is capped and charged like the VM, so constant folding cannot run
	// away (gno's keeper does the same per transaction).
	pa := gno.NewAllocator(v.capBytes)
	pa.SetGasMeter(v.meter)
	v.tx.SetPreprocessAllocator(pa)
	defer v.tx.SetPreprocessAllocator(nil)

	parsed, err := v.parse(e.Seq, e.Ops)
	if err == nil {
		err = v.checkNames(parsed)
	}
	if err != nil {
		return "", v.meter.used(), err
	}
	value, err = v.run(parsed, render)
	return value, v.meter.used(), err
}

// memory reports the bytes the VM's allocator holds and its cap.
func (v *vm) memory() (used, capBytes int64) {
	capBytes, used = v.alloc.Status()
	return used, capBytes
}

// execContext is the chain context a VM runs under. There is no chain: the
// packages that need a banker or chain parameters cannot be imported (see
// unavailablePackage), and events go to a logger nobody reads. Height is the
// entry's sequence number and the time is the entry's recorded time, so
// time.Now gives the same answer when the journal is replayed.
func execContext(seq int64, t time.Time) stdlibs.ExecContext {
	return stdlibs.ExecContext{
		ChainID:       "klash",
		ChainDomain:   "klash.local",
		Height:        seq,
		Timestamp:     t.Unix(),
		TimestampNano: int64(t.Nanosecond()),
		EventLogger:   sdk.NewEventLogger(),
	}
}

// renderValues renders evaluated values by their structure only: no user
// String or Error method runs. A string is quoted, so "1" and 1 differ.
func renderValues(tvs []gno.TypedValue) string {
	parts := make([]string, 0, len(tvs))
	for _, tv := range tvs {
		parts = append(parts, renderValue(tv))
	}
	return strings.Join(parts, ", ")
}

func renderValue(tv gno.TypedValue) string { return renderAt(tv, 0) }

// renderAt follows one pointer, so &P{1, 2} shows as that rather than as a
// bare pointer kind, and names a struct's declared type.
func renderAt(tv gno.TypedValue, depth int) string {
	// A package variable a closure captures is boxed in a heap item.
	if hiv, ok := tv.V.(*gno.HeapItemValue); ok {
		tv = hiv.Value
	}
	if tv.T == nil {
		return "nil" // the GnoVM says "undefined"
	}
	switch tv.T.Kind() {
	case gno.StringKind:
		s := tv.GetString()
		if len(s) > maxValue {
			return strconv.Quote(s[:maxValue]) + fmt.Sprintf("… (%d bytes)", len(s))
		}
		return strconv.Quote(s)
	case gno.PointerKind:
		if pv, ok := tv.V.(gno.PointerValue); ok && pv.TV != nil && depth == 0 {
			return "&" + renderAt(pv.Deref(), depth+1)
		}
	}
	s := gno.BoundedSprintTV(tv, nil, maxValue)
	if dt, ok := tv.T.(*gno.DeclaredType); ok && tv.T.Kind() == gno.StructKind {
		return string(dt.Name) + s
	}
	return s
}

// capWriter keeps the first max bytes written to it and counts the rest. It
// never fails, so a program printing a lot is not stopped by its output.
type capWriter struct {
	max   int
	buf   []byte
	total int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.total += int64(len(p))
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (w *capWriter) reset() { w.buf, w.total = w.buf[:0], 0 }

func (w *capWriter) String() string {
	if dropped := w.total - int64(len(w.buf)); dropped > 0 {
		return string(w.buf) + fmt.Sprintf("\n… (%d more bytes of output not shown)", dropped)
	}
	return string(w.buf)
}
