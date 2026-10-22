package log

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// lines decodes the JSON log lines in buf.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		out = append(out, m)
	}
	return out
}

// What config loading logs before Setup reaches the log file once Setup
// runs: in order, with its attributes and groups, and without the debug
// lines when debug logging is off.
func TestEarlyRecordsReachTheFinalHandler(t *testing.T) {
	t.Parallel()

	h := newEarlyHandler(10)
	logger := slog.New(h)
	logger.Warn("Skipping provider due to missing API key", "provider", "openai")
	logger.Debug("resolving providers")
	logger.With("path", "/p/crushrc").WithGroup("merge").Info("Found both a JSON config and a crushrc", "keys", 2)

	var buf bytes.Buffer
	h.buf.drainTo(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	got := lines(t, &buf)
	require.Len(t, got, 2, "the debug line is below the final level")
	require.Equal(t, "Skipping provider due to missing API key", got[0]["msg"])
	require.Equal(t, "WARN", got[0]["level"])
	require.Equal(t, "openai", got[0]["provider"])
	require.Equal(t, "Found both a JSON config and a crushrc", got[1]["msg"])
	require.Equal(t, "/p/crushrc", got[1]["path"])
	require.Equal(t, map[string]any{"keys": float64(2)}, got[1]["merge"])
}

// A logger made from the early handler before Setup keeps logging, to the
// final handler, after it; before, the discard handler kept such a logger
// silent for the life of the process.
func TestEarlyLoggerForwardsAfterDrain(t *testing.T) {
	t.Parallel()

	h := newEarlyHandler(10)
	kept := slog.New(h).With("component", "config")

	var buf bytes.Buffer
	h.buf.drainTo(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	require.Empty(t, lines(t, &buf))

	require.False(t, kept.Enabled(t.Context(), slog.LevelDebug), "the final level applies")
	kept.Warn("late")
	got := lines(t, &buf)
	require.Len(t, got, 1)
	require.Equal(t, "late", got[0]["msg"])
	require.Equal(t, "config", got[0]["component"])
}

// The buffer is bounded; what did not fit is counted in the log.
func TestEarlyRecordsAreBounded(t *testing.T) {
	t.Parallel()

	h := newEarlyHandler(3)
	logger := slog.New(h)
	for range 5 {
		logger.Info("line")
	}

	var buf bytes.Buffer
	h.buf.drainTo(slog.NewJSONHandler(&buf, nil))

	got := lines(t, &buf)
	require.Len(t, got, 4)
	require.Equal(t, "Log records from before logging was set up were dropped", got[3]["msg"])
	require.Equal(t, float64(2), got[3]["dropped"])
	require.Equal(t, float64(3), got[3]["kept"])
}
