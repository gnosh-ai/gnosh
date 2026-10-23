package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// The fetch agent's own fetches reach public sites only: a page it read
// cannot send it to a service on this machine or network (the model
// server, a router) or to the cloud metadata address and have it read
// what is there. A fetch through the plain client, as the one the user
// approved, still reaches it.
func TestPublicOnlyRefusesLocalAddresses(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "TOPSECRET local service")
	}))
	t.Cleanup(srv.Close)
	fetch := func(client *http.Client) fantasy.ToolResponse {
		t.Helper()
		input, err := json.Marshal(WebFetchParams{URL: srv.URL})
		require.NoError(t, err)
		resp, err := NewWebFetchTool(t.TempDir(), client).Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: WebFetchToolName, Input: string(input)})
		require.NoError(t, err)
		return resp
	}

	resp := fetch(PublicOnly(srv.Client()))
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "local or private address")
	require.NotContains(t, resp.Content, "TOPSECRET")

	require.Contains(t, fetch(srv.Client()).Content, "TOPSECRET", "the plain client is untouched")

	for _, addr := range []string{"127.0.0.1", "10.0.0.5", "172.16.1.1", "192.168.1.91", "169.254.169.254", "100.100.1.1", "0.1.2.3", "0.0.0.0", "224.0.0.1", "::1", "fe80::1", "fd00::1", "::ffff:127.0.0.1"} {
		require.Falsef(t, isPublicAddr(netip.MustParseAddr(addr)), addr)
	}
	for _, addr := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700::1111"} {
		require.Truef(t, isPublicAddr(netip.MustParseAddr(addr)), addr)
	}
}
