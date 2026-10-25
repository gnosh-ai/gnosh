package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

// BranchSession copies the conversation of session sourceID into a new
// session, as Claude Code's /branch does, and returns the new one: every
// message in order, with its times and the steps of the sub-agents it ran,
// the files it read and changed, the summary the session was compacted to,
// its todos and how full its context is. The new session is titled title,
// or by branchTitle after the source. The source is left as it was.
func (a *App) BranchSession(ctx context.Context, sourceID, title string) (session.Session, error) {
	if err := a.Messages.FlushAll(ctx); err != nil {
		return session.Session{}, err
	}
	source, err := a.Sessions.Get(ctx, sourceID)
	if err != nil {
		return session.Session{}, err
	}
	messages, err := a.Messages.List(ctx, sourceID)
	if err != nil {
		return session.Session{}, err
	}
	if len(messages) == 0 {
		return session.Session{}, errors.New("no messages to branch")
	}
	if title == "" {
		sessions, err := a.Sessions.List(ctx)
		if err != nil {
			return session.Session{}, err
		}
		titles := make([]string, 0, len(sessions))
		for _, s := range sessions {
			titles = append(titles, s.Title)
		}
		title = branchTitle(cmp.Or(source.Title, "Branched conversation"), titles)
	}
	branch, err := a.Sessions.Create(ctx, title)
	if err != nil {
		return session.Session{}, err
	}
	ids, err := a.copyConversation(ctx, messages, sourceID, branch.ID)
	if err != nil {
		// Half a branch is no branch.
		_ = a.Sessions.Delete(context.WithoutCancel(ctx), branch.ID)
		return session.Session{}, fmt.Errorf("copying the conversation: %w", err)
	}
	branch.SummaryMessageID = ids[source.SummaryMessageID]
	branch.Todos = source.Todos
	branch.PromptTokens, branch.CompletionTokens = source.PromptTokens, source.CompletionTokens
	branch.EstimatedUsage = source.EstimatedUsage
	return a.Sessions.Save(ctx, branch)
}

// copyConversation copies the conversation of session from, whose
// messages are messages, into session to: the messages, with the steps of
// the sub-agents they ran, the files it read and the versions of the files
// it changed. It returns the copied messages' IDs by the IDs they were
// copied from.
func (a *App) copyConversation(ctx context.Context, messages []message.Message, from, to string) (map[string]string, error) {
	ids, err := a.copyMessages(ctx, messages, to)
	if err != nil {
		return nil, err
	}
	if err := a.FileTracker.CopyReads(ctx, from, to); err != nil {
		return nil, err
	}
	if err := a.History.CopySessionFiles(ctx, from, to); err != nil {
		return nil, err
	}
	return ids, nil
}

// copyMessages copies messages into session sessionID, and with them the
// sessions of the sub-agents their tool calls ran, keyed by the copies, so
// the branch shows each sub-agent's steps as the source does. It returns
// the copies' IDs by the IDs they were copied from.
func (a *App) copyMessages(ctx context.Context, messages []message.Message, sessionID string) (map[string]string, error) {
	ids := make(map[string]string, len(messages))
	for _, msg := range messages {
		copied, err := a.Messages.Copy(ctx, sessionID, msg)
		if err != nil {
			return nil, err
		}
		ids[msg.ID] = copied.ID
		for _, tc := range msg.ToolCalls() {
			sub, err := a.Sessions.Get(ctx, a.Sessions.CreateAgentToolSessionID(msg.ID, tc.ID))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			subMessages, err := a.Messages.List(ctx, sub.ID)
			if err != nil {
				return nil, err
			}
			subCopy, err := a.Sessions.CreateTaskSession(ctx, a.Sessions.CreateAgentToolSessionID(copied.ID, tc.ID), sessionID, sub.Title)
			if err != nil {
				return nil, err
			}
			if _, err := a.copyMessages(ctx, subMessages, subCopy.ID); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

// branchTitle is what Claude Code titles a branch of a session titled
// base: "base (Branch)", or "base (Branch N)" with the first N from 2 that
// no title in taken has.
func branchTitle(base string, taken []string) string {
	title := base + " (Branch)"
	for n := 2; slices.Contains(taken, title); n++ {
		title = fmt.Sprintf("%s (Branch %d)", base, n)
	}
	return title
}
