package question

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretIsOnlyForFreeText(t *testing.T) {
	t.Parallel()
	q := Question{Type: TypeYesNo, Text: "Password?", Description: "Typed blind.", Secret: true}
	require.ErrorContains(t, q.Validate(), "only a free_text question can be secret")
	q.Type = TypeFreeText
	require.NoError(t, q.Validate())
}
