package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// klash run --output-format json prints one result object at the end, as
// Claude Code's -p --output-format json does, so a script gets the answer
// and the session to resume without parsing streamed text.
func TestRunOutputJSON(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	out, err := newRunOutput("json", &stdout)
	require.NoError(t, err)
	fmt.Fprint(out.w, "hi there\n")
	out.sessionID = "abc"
	require.NoError(t, out.finish(nil))

	var res map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &res), stdout.String())
	require.Equal(t, "result", res["type"])
	require.Equal(t, "success", res["subtype"])
	require.Equal(t, false, res["is_error"])
	require.Equal(t, "hi there", res["result"])
	require.Equal(t, "abc", res["session_id"])
	require.Contains(t, res, "duration_ms")

	// A failed run still prints its object, and still fails.
	stdout.Reset()
	out, err = newRunOutput("json", &stdout)
	require.NoError(t, err)
	runErr := errors.New("model not found")
	require.ErrorIs(t, out.finish(runErr), runErr)
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &res))
	require.Equal(t, true, res["is_error"])
	require.Equal(t, "error_during_execution", res["subtype"])
	require.Equal(t, "model not found", res["result"])
}

func TestRunOutputText(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	out, err := newRunOutput("text", &stdout)
	require.NoError(t, err)
	fmt.Fprint(out.w, "streamed")
	require.NoError(t, out.finish(nil))
	require.Equal(t, "streamed", stdout.String(), "text streams the answer and adds nothing")

	_, err = newRunOutput("stream-json", &stdout)
	require.ErrorContains(t, err, "not supported yet")
	_, err = newRunOutput("yaml", &stdout)
	require.ErrorContains(t, err, "expected text or json")
}

func TestRunTakesOutputFormat(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "run"}
	addRunFlags(cmd)
	require.NoError(t, cmd.ParseFlags([]string{"--output-format", "json", "hi"}))
	got, err := cmd.Flags().GetString("output-format")
	require.NoError(t, err)
	require.Equal(t, "json", got)
}
