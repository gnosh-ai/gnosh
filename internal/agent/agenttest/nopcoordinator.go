package agenttest

import (
	"context"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
)

// NopCoordinator implements every [agent.Coordinator] method as a no-op
// returning zero values. Test stubs embed it and override only the
// methods they actually exercise.
//
// Before this existed, each package's stubs spelled out the whole
// interface by hand, so a method added to agent.Coordinator broke every
// one of them at once. That is what happened with ToolInfos and
// ListSettledMessages: the tests in internal/backend and internal/server
// stopped compiling, and a package whose tests do not compile is a
// package whose tests do not run — silently, because `go test ./...`
// reports it as a build failure among the passes.
//
// With the interface satisfied in one place, adding a method here is the
// single edit that keeps every stub compiling.
type NopCoordinator struct{}

var _ agent.Coordinator = (*NopCoordinator)(nil)

func (*NopCoordinator) SetMainAgent(string) error { return nil }

func (*NopCoordinator) Run(context.Context, string, string, ...message.Attachment) (*fantasy.AgentResult, error) {
	return nil, nil
}

func (*NopCoordinator) RunAccepted(context.Context, *agent.AcceptedRun, string, string, ...message.Attachment) (*fantasy.AgentResult, error) {
	return nil, nil
}

func (*NopCoordinator) BeginAccepted(string) *agent.AcceptedRun { return nil }
func (*NopCoordinator) Cancel(string)                           {}
func (*NopCoordinator) CancelAll()                              {}
func (*NopCoordinator) IsSessionBusy(string) bool               { return false }
func (*NopCoordinator) IsBusy() bool                            { return false }

func (*NopCoordinator) ListSettledMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (*NopCoordinator) QueuedPrompts(string) int                        { return 0 }
func (*NopCoordinator) QueuedPromptsList(string) []string               { return nil }
func (*NopCoordinator) PopQueuedPrompts(string) []message.PoppedPrompt  { return nil }
func (*NopCoordinator) ClearQueue(string)                               {}
func (*NopCoordinator) Summarize(context.Context, string, string) error { return nil }
func (*NopCoordinator) Model() agent.Model                              { return agent.Model{} }
func (*NopCoordinator) UpdateModels(context.Context) error              { return nil }
func (*NopCoordinator) GenerateTitle(context.Context, string, string)   {}

func (*NopCoordinator) GenerateSessionName(context.Context, string) (string, error) {
	return "", nil
}

func (*NopCoordinator) ContextUsage(context.Context, string) (agent.ContextUsage, error) {
	return agent.ContextUsage{}, nil
}

func (*NopCoordinator) ToolInfos(context.Context) ([]fantasy.ToolInfo, error) {
	return nil, nil
}

func (*NopCoordinator) ToolNames() []string { return nil }

func (*NopCoordinator) Rewound(string, []string) {}

func (*NopCoordinator) Branched(string) {}

func (*NopCoordinator) RebuildSystemPrompts(context.Context) error { return nil }

func (*NopCoordinator) SetGoal(context.Context, string, string) (agent.GoalChange, error) {
	return agent.GoalChange{}, nil
}

func (*NopCoordinator) Goal(context.Context, string) (agent.Goal, bool) { return agent.Goal{}, false }

func (*NopCoordinator) ClearGoal(context.Context, string) (agent.GoalChange, error) {
	return agent.GoalChange{}, nil
}
