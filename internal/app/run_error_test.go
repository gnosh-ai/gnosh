package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/stretchr/testify/require"
)

func TestRunError(t *testing.T) {
	t.Parallel()

	blocked := fmt.Errorf("%w: no secrets in prompts", agent.ErrPromptBlocked)
	require.Equal(t, "prompt blocked by a UserPromptSubmit hook: no secrets in prompts", runError(blocked).Error(),
		"a blocked prompt is the hook's refusal, not a failure")

	err := runError(errors.New("model server returned 500"))
	require.Equal(t, "agent processing failed: model server returned 500", err.Error(),
		"an error from anywhere in the turn is not called a failure to start")
}
