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
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A mem rebuilt from its journal is the mem that was live: after random
// calls of every kind by two sessions and the user, from a small shared
// vocabulary so that they meet, with forgets, redacts and undos among them, a
// repair (which deletes the store and the index) and a rebuild show and
// answer JSON exactly as before, and pass Check.
func TestMemRebuildMatchesLive(t *testing.T) {
	t.Parallel()
	seeds := int64(2)
	if n, err := strconv.ParseInt(os.Getenv("GNOMEM_REBUILD_SEEDS"), 10, 64); err == nil {
		seeds = n // more, to search
	}
	for seed := int64(1); seed <= seeds; seed++ {
		t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) {
			t.Parallel()
			testMemRebuildMatchesLive(t, seed, 100)
		})
	}
}

func testMemRebuildMatchesLive(t *testing.T, seed int64, calls int) {
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	vocab := strings.Fields("tea kettle deploy key token build test release docs cache alpha beta")
	words := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = vocab[rng.Intn(len(vocab))]
		}
		return strings.Join(w, " ")
	}
	goals, facts, rules := 0, 0, 0
	goal := func() string { return "g" + strconv.Itoa(1+rng.Intn(max(goals, 1))) }
	item := func() string {
		name := goal()
		for depth := rng.Intn(3); depth > 0; depth-- {
			name += "/" + strconv.Itoa(1+rng.Intn(3))
		}
		return name
	}
	fact := func() string { return "f/" + strconv.Itoa(1+rng.Intn(max(facts, 1))) }
	var secrets []string
	var last int64
	kept, failed, undone := 0, 0, 0
	for i := 0; i < calls; i++ {
		who, code := "s1", ""
		if rng.Intn(3) == 0 {
			who = "s2"
		}
		switch rng.Intn(16) {
		case 0, 1:
			goals++
			steps := make([]string, rng.Intn(4))
			for j := range steps {
				steps[j] = strconv.Quote(words(2))
			}
			code = fmt.Sprintf("mem.Goal(%q, %q)", words(3), "g"+strconv.Itoa(goals))
			if len(steps) > 0 {
				code += ".Steps(" + strings.Join(steps, ", ") + ")"
			}
		case 2:
			code = fmt.Sprintf("mem.Steps(%q, %q)", item(), words(2))
		case 3, 4:
			code = fmt.Sprintf("mem.Done(%q, %q)", item(), words(2))
		case 5:
			code = fmt.Sprintf("mem.Drop(%q, %q)", item(), words(2))
		case 6:
			code = fmt.Sprintf("mem.Reopen(%q)", item())
		case 7:
			note := words(3)
			if rng.Intn(4) == 0 {
				secret := "sk-" + strconv.Itoa(rng.Intn(1e6)) + "-zq"
				secrets = append(secrets, secret)
				note += " " + secret
			}
			code = fmt.Sprintf("mem.Note(%q, %q)", item(), note)
		case 8:
			until := []string{"", "2026-11-03", "2026-12-01", "the " + words(1) + " lands"}[rng.Intn(4)]
			code = fmt.Sprintf("mem.Wait(%q, %q)", item(), until)
		case 9:
			code = fmt.Sprintf("mem.Ask(%q, %q)", goal(), words(3)+"?")
		case 10:
			code = fmt.Sprintf("mem.Start(%q)", item())
		case 11:
			if rng.Intn(3) == 0 && facts > 0 {
				code = fmt.Sprintf("mem.Fact(%q, \"\")", fact())
			} else {
				facts++
				code = fmt.Sprintf("mem.Fact(%q, %q)", "f/"+strconv.Itoa(facts), words(3))
			}
		case 12:
			who = "user"
			rules++
			code = fmt.Sprintf("mem.Rule(%q, %q)", "r/"+strconv.Itoa(rules), "always "+words(2))
		case 13:
			if rng.Intn(2) == 0 {
				code = fmt.Sprintf("mem.Forget(%q)", fact())
			} else {
				code = fmt.Sprintf("mem.Forget(%q)", item())
			}
		case 14:
			if len(secrets) == 0 {
				continue
			}
			who = "user"
			code = fmt.Sprintf("mem.Redact(%q)", secrets[rng.Intn(len(secrets))])
		case 15:
			if last == 0 {
				continue
			}
			out, err := h.undo(ctx, "s9", 1+rng.Int63n(last))
			require.NoError(t, err)
			if strings.HasPrefix(out, "undid") {
				last++
				kept++
				undone++
			} else {
				failed++
			}
			continue
		}
		q := memRequest{Code: code, Session: who}
		if who == "user" {
			q = memRequest{Code: code, User: true}
		}
		res, err := h.call(ctx, q)
		if err != nil {
			failed++
			continue
		}
		kept++
		last = max(last, res.Seq)
	}
	t.Logf("seed %d: %d calls kept or read (%d undos), %d refused", seed, kept, undone, failed)
	require.Greater(t, kept, calls/4, "many calls should do something")

	var names []string
	for g := 1; g <= goals; g++ {
		names = append(names, "g"+strconv.Itoa(g))
		for s := 1; s <= 3; s++ {
			names = append(names, "g"+strconv.Itoa(g)+"/"+strconv.Itoa(s))
		}
	}
	for f := 1; f <= facts; f++ {
		names = append(names, "f/"+strconv.Itoa(f))
	}
	for r := 1; r <= rules; r++ {
		names = append(names, "r/"+strconv.Itoa(r))
	}
	// Both dumps are taken at one time, so "ago" and today agree.
	at := time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return at }
	dump := func() string {
		t.Helper()
		var sb strings.Builder
		for _, code := range []string{`print(mem.Check())`, `println(mem.Show())`, `println(mem.JSON())`} {
			res, err := h.call(ctx, memRequest{Code: code, Session: "s1"})
			require.NoError(t, err, code)
			sb.WriteString(res.Output)
		}
		for _, n := range names {
			res, err := h.call(ctx, memRequest{Code: fmt.Sprintf("println(mem.Show(%q))", n), Session: "s1"})
			require.NoError(t, err, n)
			sb.WriteString(res.Output)
		}
		return sb.String()
	}
	live := dump()
	require.NotContains(t, live, "check:", "the live mem passes Check")
	_, err := h.repair(ctx)
	require.NoError(t, err)
	require.Equal(t, live, dump(), "a mem rebuilt from its journal is the mem that was live")
}
