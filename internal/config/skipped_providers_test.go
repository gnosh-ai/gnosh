package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

// A provider the user configured but loading had to leave out is recorded
// with why, so "no providers configured" does not send someone whose model
// server is down to set a provider up again. Providers from the catalog
// the user never configured are not listed.
func TestSkippedProvidersSayWhy(t *testing.T) {
	t.Parallel()

	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down.Close()

	cfg := &Config{Providers: csync.NewMap[string, ProviderConfig]()}
	cfg.Providers.Set("local", ProviderConfig{ID: "local", Type: catwalk.TypeOpenAICompat, BaseURL: down.URL})
	cfg.Providers.Set("nourl", ProviderConfig{ID: "nourl", Type: catwalk.TypeOpenAICompat, Models: []catwalk.Model{{ID: "m"}}})
	cfg.Providers.Set("openai", ProviderConfig{})
	cfg.setDefaults(t.TempDir(), "")
	known := []catwalk.Provider{
		{ID: "openai", APIKey: "$OPENAI_API_KEY", APIEndpoint: "https://api.openai.com/v1", Models: []catwalk.Model{{ID: "m"}}},
		{ID: "anthropic", APIKey: "$ANTHROPIC_API_KEY", APIEndpoint: "https://api.anthropic.com/v1", Models: []catwalk.Model{{ID: "m"}}},
	}
	e := env.NewFromMap(map[string]string{})
	require.NoError(t, cfg.configureProviders(context.Background(), testStore(cfg), e, NewShellVariableResolver(e), known))

	require.False(t, cfg.IsConfigured())
	skipped := cfg.SkippedProviders()
	require.Len(t, skipped, 3, "anthropic was never configured, so it is not listed")
	require.Contains(t, skipped["local"], "its model server did not answer")
	require.Equal(t, "it has no base_url", skipped["nourl"])
	require.Equal(t, "it has no API key", skipped["openai"])
	require.Contains(t, cfg.SkippedProvidersSummary(), "local: its model server did not answer")
}
