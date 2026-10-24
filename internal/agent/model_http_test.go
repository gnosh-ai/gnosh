package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/stretchr/testify/require"
)

func TestLocalModelServer(t *testing.T) {
	t.Parallel()

	for _, baseURL := range []string{
		"http://127.0.0.1:8080/v1",
		"http://localhost:11434/v1",
		"http://LocalHost:1234",
		"http://localhost.:8080",
		"http://[::1]:8080/v1",
		"http://192.168.1.91:8080/v1",
		"http://10.0.0.5",
		"http://172.16.3.4:8000",
		"http://169.254.1.1",
		"http://100.101.102.103:8080",
		"http://aibox:8080/v1",
		"http://box.local:8080",
		"http://gpu.lan/v1",
		"http://llm.internal",
		"http://x.home.arpa",
		"http://api.localhost:8080",
	} {
		require.True(t, localModelServer(baseURL), baseURL)
	}
	for _, baseURL := range []string{
		"",
		"https://api.openai.com/v1",
		"https://api.anthropic.com",
		"https://openrouter.ai/api/v1",
		"http://8.8.8.8",
		"http://100.128.0.1",
		"http://172.32.0.1",
		"https://local.example.com",
		"https://internal.example.com",
		"https://example.lan.evil.com",
		"://bad",
	} {
		require.False(t, localModelServer(baseURL), baseURL)
	}
}

func TestModelResponseTimeout(t *testing.T) {
	const local, remote = "http://127.0.0.1:8080/v1", "https://api.openai.com/v1"
	t.Setenv("API_TIMEOUT_MS", "")

	require.Zero(t, modelResponseTimeout(local), "a local server has no limit")
	require.Equal(t, 10*time.Minute, modelResponseTimeout(remote))

	t.Setenv("API_TIMEOUT_MS", "1500")
	require.Equal(t, 1500*time.Millisecond, modelResponseTimeout(local))
	require.Equal(t, 1500*time.Millisecond, modelResponseTimeout(remote))

	for _, bad := range []string{"0", "-5", "ten", ""} {
		t.Setenv("API_TIMEOUT_MS", bad)
		require.Zero(t, modelResponseTimeout(local), bad)
		require.Equal(t, 10*time.Minute, modelResponseTimeout(remote), bad)
	}
}

func TestModelHTTPClientCarriesTheTimeout(t *testing.T) {
	t.Setenv("API_TIMEOUT_MS", "")
	env := testEnv(t)
	coord := newTestCoordinator(t, env, "local", config.ProviderConfig{})

	headerTimeout := func(client *http.Client) time.Duration {
		transport := client.Transport
		if logger, ok := transport.(*log.HTTPRoundTripLogger); ok {
			transport = logger.Transport
		}
		return transport.(*http.Transport).ResponseHeaderTimeout
	}
	require.Zero(t, headerTimeout(coord.modelHTTPClient("http://127.0.0.1:8080/v1")))
	require.Equal(t, 10*time.Minute, headerTimeout(coord.modelHTTPClient("https://api.openai.com/v1")))

	coord.cfg.Config().Options.Debug = true
	client := coord.modelHTTPClient("http://127.0.0.1:8080/v1")
	require.IsType(t, &log.HTTPRoundTripLogger{}, client.Transport, "debug mode logs requests")
	require.Zero(t, headerTimeout(client))
}

// A model server that has not started answering: each kind of provider
// gives up on it after API_TIMEOUT_MS, so the client built above is the
// one the provider uses.
func TestProvidersUseTheModelHTTPClient(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		http.Error(w, "too late", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	t.Setenv("API_TIMEOUT_MS", "200")

	for _, providerType := range []catwalk.Type{catwalk.TypeOpenAICompat, catwalk.TypeOpenAI, catwalk.TypeAnthropic} {
		t.Run(string(providerType), func(t *testing.T) {
			env := testEnv(t)
			providerCfg := config.ProviderConfig{
				ID:      "box",
				Type:    providerType,
				BaseURL: srv.URL + "/v1",
				APIKey:  "test",
			}
			coord := newTestCoordinator(t, env, "box", providerCfg)
			provider, err := coord.buildProvider(providerCfg, config.SelectedModel{Model: "m", Provider: "box"}, false)
			require.NoError(t, err)
			model, err := provider.LanguageModel(t.Context(), "m")
			require.NoError(t, err)

			start := time.Now()
			_, err = model.Generate(t.Context(), fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hi")}})
			require.Error(t, err)
			require.Contains(t, err.Error(), "timeout awaiting response headers")
			require.Less(t, time.Since(start), 4*time.Second)
		})
	}
}
