// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package goals

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckEval(t *testing.T) {
	t.Parallel()
	facts := map[string]string{"tests": "pass", "coverage": "81.5", "failures": "0", "notes": "abc"}
	for src, want := range map[string]bool{
		`facts["tests"] == "pass"`:                                true,
		`num(facts["coverage"]) >= 80`:                            true,
		`num(facts["coverage"]) >= 90`:                            false,
		`num(facts["failures"]) == 0 && facts["tests"] == "pass"`: true,
		`has("deployed") || facts["tests"] == "pass"`:             true,
		`!has("deployed")`:                                        true,
		`has("deployed") && facts["deployed"] == "yes"`:           false, // short-circuits: no error
		`len(facts["notes"]) > 2`:                                 true,
		`-num(facts["coverage"]) < -80`:                           true,
		`(facts["tests"] == "pass") == true`:                      true,
		`facts["tests"] != "fail"`:                                true,
		`facts["notes"] < "abd"`:                                  true,
	} {
		c, err := ParseCheck(src)
		require.NoError(t, err, src)
		got, err := c.Eval(facts)
		require.NoError(t, err, src)
		require.Equal(t, want, got, src)
	}
}

func TestCheckErrorsAtEval(t *testing.T) {
	t.Parallel()
	c, err := ParseCheck(`facts["tests"] == "pass"`)
	require.NoError(t, err)
	_, err = c.Eval(map[string]string{})
	require.ErrorContains(t, err, `fact "tests" is not recorded yet`)

	c, err = ParseCheck(`num(facts["coverage"]) > 50`)
	require.NoError(t, err)
	_, err = c.Eval(map[string]string{"coverage": "lots"})
	require.ErrorContains(t, err, "not a number")
	require.Equal(t, []string{"coverage"}, c.Keys())
}

// The language admits nothing but facts, literals and the operators: no
// model text can become code.
func TestCheckRefusesEverythingElse(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		``,
		`true`,                            // refers to no fact
		`facts["a"]`,                      // not a condition
		`facts["a"] == 1`,                 // string vs number
		`facts["a"] > true`,               // ordering bools
		`num(facts["a"]) + 1 > 2`,         // no arithmetic
		`G.Close("g1", "met")`,            // no calls but num, len, has
		`func() bool { return true }()`,   // no function literals
		`facts[x] == "y"`,                 // the key must be quoted
		`facts["Bad Key"] == "y"`,         // key shape
		`has(k)`,                          // has takes a quoted key
		`os.Exit(1) == nil`,               // no selectors
		`facts["a"] == "b"; panic(1)`,     // one expression only
		`len(facts["a"], facts["b"]) > 0`, // one argument
		`num(1) > 0`,                      // num takes a string
		`!facts["a"]`,                     // ! needs a bool
		`facts["a"] =~ "b"`,               // not Go
		`[]string{"a"}[0] == "a"`,         // no composite literals
		string(make([]byte, MaxCheck+1)),  // too long
	} {
		_, err := ParseCheck(src)
		require.Error(t, err, src)
	}
}
