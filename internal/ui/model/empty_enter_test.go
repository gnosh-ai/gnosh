package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/util"
)

func enterKey() tea.Msg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
}

// infoMsgs runs cmd's tree and returns the info messages it reports.
func infoMsgs(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []string
		for _, c := range msg {
			out = append(out, infoMsgs(c)...)
		}
		return out
	case util.InfoMsg:
		return []string{msg.Msg}
	}
	return nil
}

// Enter with an image attached and no text sends nothing, because the
// agent refuses a prompt that is only an image. The image used to be
// cleared from the editor all the same, so it was lost; it now stays for
// the prompt the user types, and Enter says what is missing.
func TestEnterWithOnlyAnImageKeepsTheImage(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	shot := message.Attachment{FileName: "shot.png", FilePath: "shot.png", MimeType: "image/png", Content: []byte("png")}
	m.attachments.Update(shot)

	_, cmd := m.Update(enterKey())
	require.Equal(t, []string{"Type a prompt to send with the attachment"}, infoMsgs(cmd))
	require.Equal(t, []message.Attachment{shot}, m.attachments.List(), "the image must still be in the editor")
	require.Empty(t, ws.runs, "nothing is sent")

	// With words typed, Enter sends them with the image and clears it.
	m.textarea.InsertString("what is in this picture?")
	_, cmd = m.Update(enterKey())
	runCmds(m, cmd)
	require.Equal(t, []string{"s1: what is in this picture?"}, ws.runs)
	require.Empty(t, m.attachments.List(), "sent with the prompt, the image leaves the editor")
}
