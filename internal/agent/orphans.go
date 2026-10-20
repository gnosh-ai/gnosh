package agent

import (
	"context"
	"log/slog"

	"github.com/charmbracelet/crush/internal/message"
)

// orphanNote is the finish message stored on an assistant message that was
// left unfinished by a crash or a hard kill, so the UI can say why.
const orphanNote = "Interrupted: Crush was stopped before this reply finished"

// orphanStore is the slice of the message service the repair needs.
type orphanStore interface {
	List(ctx context.Context, sessionID string) ([]message.Message, error)
	Update(ctx context.Context, msg message.Message) error
}

// finalizeOrphanedAssistants gives a Finish part to every assistant message
// of the session that never got one, and persists it.
//
// A turn that ends normally, errors, or is cancelled always writes a Finish
// part. The only way an assistant message is left without one is a process
// that died mid-turn (SIGKILL, crash, power loss). Such a message is a
// ghost: on every reload the UI renders it as still working, and once new
// work starts for the session its spinner animates again next to the real
// turn's, so the user sees two "working" lines and a turn that never ends.
//
// It is called at the start of a run, when no other run can be active for
// the session, so every unfinished assistant message it finds is an orphan,
// never the live turn. The whole transcript is scanned, not just the tail
// after a summary, because the UI shows the whole transcript. Failures are
// logged and the run proceeds: the repair is cosmetic for the model (the
// prompt builder already tolerates unfinished messages) and the next run
// retries it.
func finalizeOrphanedAssistants(ctx context.Context, store orphanStore, sessionID string) int {
	msgs, err := store.List(ctx, sessionID)
	if err != nil {
		slog.Warn("Could not list messages to repair unfinished replies", "session", sessionID, "error", err)
		return 0
	}
	repaired := 0
	for i := range msgs {
		msg := msgs[i]
		if msg.Role != message.Assistant || msg.IsFinished() {
			continue
		}
		msg.AddFinish(message.FinishReasonCanceled, orphanNote, "")
		if err := store.Update(ctx, msg); err != nil {
			slog.Warn("Could not finalize an unfinished reply", "session", sessionID, "message", msg.ID, "error", err)
			continue
		}
		repaired++
	}
	if repaired > 0 {
		slog.Info("Finalized unfinished replies left by an interrupted run", "session", sessionID, "count", repaired)
	}
	return repaired
}
