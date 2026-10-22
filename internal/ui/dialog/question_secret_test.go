package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newTestSecret() *Secret {
	s := styles.CharmtonePantera()
	d := NewSecret(&s, question.Question{
		ID:          "q1",
		Type:        question.TypeFreeText,
		Secret:      true,
		Text:        "Password for key alice",
		Description: "Signs and runs: `gnokey sign -tx-path tx.json alice`",
	})
	d.SetFocused(true)
	return d
}

func typeInto(t *testing.T, d *Secret, text string) {
	t.Helper()
	for _, r := range text {
		done, _ := d.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		require.False(t, done)
	}
}

func TestSecretMasksWhatIsTyped(t *testing.T) {
	t.Parallel()
	d := newTestSecret()
	typeInto(t, d, "hunter2")

	lines, row := d.lines(80)
	require.NotContains(t, strings.Join(lines, "\n"), "hunter2")
	require.Contains(t, lines[row], strings.Repeat("•", len("hunter2")))
	require.Contains(t, lines[0], "Password for key alice")

	done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, done)
	require.Equal(t, question.Answer{QuestionID: "q1", FillInText: "hunter2"}, d.Response())
}

func TestSecretKeepsSpaces(t *testing.T) {
	t.Parallel()
	d := newTestSecret()
	typeInto(t, d, " two words ")
	done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, done)
	require.Equal(t, " two words ", d.Response().FillInText)
}

func TestSecretRefusesAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	d := newTestSecret()
	done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.False(t, done)
	require.Equal(t, question.Answer{}, d.Response(), "nothing is answered until enter on a non-empty line")
}

func TestSecretResponseIsNotWhatIsBeingTyped(t *testing.T) {
	t.Parallel()
	d := newTestSecret()
	typeInto(t, d, "half")
	require.Equal(t, question.Answer{}, d.Response())
}

func TestSecretHeightMatchesItsLines(t *testing.T) {
	t.Parallel()
	d := newTestSecret()
	lines, _ := d.lines(60)
	require.Equal(t, len(lines), d.Height(60))
}

func TestQuestionFormUsesTheSecretInputForASecretQuestion(t *testing.T) {
	t.Parallel()
	s := styles.CharmtonePantera()
	form := NewQuestionForm(&s, question.Request{Questions: []question.Question{
		{ID: "q1", Type: question.TypeFreeText, Secret: true, Text: "Password for key alice", Description: "Signs tx.json"},
		{ID: "q2", Type: question.TypeFreeText, Text: "Anything else?", Description: "Plain"},
	}})
	require.Len(t, form.questions, 2)
	require.True(t, form.hasConfirm, "two questions get a confirm tab")
	require.IsType(t, &Secret{}, form.questions[0])
	require.IsType(t, &FreeText{}, form.questions[1])
}
