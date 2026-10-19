package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/sessionweb"
	"github.com/charmbracelet/crush/internal/workspace"
)

var sessionWebCmd = &cobra.Command{
	Use:   "web",
	Short: "Serve a local page to view and edit this project's sessions by clicking and typing",
	Long: `Serves, on this machine only, a page listing the project's sessions. Each
message can be edited (its text, thinking, tool calls and results), deleted, or
a new one added at the end; "What the model gets" shows the prompt template and
the tool descriptions sent with every turn, with rough token counts. Edits go
straight into Crush's database; reopen the session in the TUI to see them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		ws, cleanup, err := setupLocalWorkspace(cmd)
		if err != nil {
			return err
		}
		defer cleanup()
		appWs, ok := ws.(*workspace.AppWorkspace)
		if !ok {
			return fmt.Errorf("unexpected workspace type %T", ws)
		}
		app := appWs.App()
		store := webStore{sessions: app.Sessions, messages: app.Messages}
		contextFn := func(ctx context.Context) (sessionweb.Context, error) {
			text, path, user := agent.EffectiveTemplate("coder.md.tpl")
			source := "built-in"
			if user {
				source = path
			}
			tools, err := app.AgentCoordinator.ToolInfos(ctx)
			if err != nil {
				return sessionweb.Context{}, err
			}
			return sessionweb.Context{Template: text, TemplateSource: source, Tools: tools}, nil
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return err
		}
		srv := &http.Server{Handler: sessionweb.New(store, contextFn), ReadHeaderTimeout: 10 * time.Second}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		cwd, _ := os.Getwd()
		fmt.Fprintf(cmd.OutOrStdout(), "Sessions of %s at http://%s/  (ctrl+c stops it)\n", cwd, ln.Addr())
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	},
}

// webStore adapts Crush's services to what the page needs; updates are
// flushed so they are on disk when the request returns.
type webStore struct {
	sessions session.Service
	messages message.Service
}

func (s webStore) ListSessions(ctx context.Context) ([]session.Session, error) {
	return s.sessions.List(ctx)
}

func (s webStore) ListMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	return s.messages.List(ctx, sessionID)
}

func (s webStore) GetMessage(ctx context.Context, id string) (message.Message, error) {
	return s.messages.Get(ctx, id)
}

func (s webStore) UpdateMessage(ctx context.Context, msg message.Message) error {
	if err := s.messages.Update(ctx, msg); err != nil {
		return err
	}
	return s.messages.Flush(ctx, msg.ID)
}

func (s webStore) DeleteMessage(ctx context.Context, id string) error {
	return s.messages.Delete(ctx, id)
}

func (s webStore) CreateMessage(ctx context.Context, sessionID string, params message.CreateMessageParams) (message.Message, error) {
	return s.messages.Create(ctx, sessionID, params)
}

func init() {
	sessionWebCmd.Flags().Int("port", 7777, "Port on 127.0.0.1 to serve on")
	sessionCmd.AddCommand(sessionWebCmd)
}
