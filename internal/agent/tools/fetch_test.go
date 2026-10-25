package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func runFetchTool(t *testing.T, client *http.Client, url string) (fantasy.ToolResponse, error) {
	t.Helper()

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	tool := NewFetchTool(&mockPermissionService{}, t.TempDir(), client)

	input, err := json.Marshal(FetchParams{URL: url, Format: "text"})
	require.NoError(t, err)

	return tool.Run(ctx, fantasy.ToolCall{
		ID:    "fetch-call",
		Name:  FetchToolName,
		Input: string(input),
	})
}

// A fetch that never completes must be reported to the model as a failed tool
// result. Returning a Go error instead ends the whole agent turn.
func TestFetchToolReportsNetworkFailuresAsToolErrors(t *testing.T) {
	t.Parallel()

	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("unused"))
		}))
		url := server.URL
		server.Close()

		resp, err := runFetchTool(t, server.Client(), url)
		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "Failed to fetch URL")
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Write([]byte("too late"))
		}))
		defer server.Close()

		client := &http.Client{Timeout: 20 * time.Millisecond}
		resp, err := runFetchTool(t, client, server.URL)
		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "Failed to fetch URL")
		require.Contains(t, resp.Content, "context deadline exceeded")
	})
}

func TestFetchToolReportsStatusCodesAsToolErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	resp, err := runFetchTool(t, server.Client(), server.URL)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Request failed with status code: 404 Not Found")
}

// A 402 carrying x402 terms tells the model what the resource charges and
// how the user can pay, rather than only that the request failed.
func TestFetchToolNamesTheX402Terms(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		required := `{"x402Version":2,"accepts":[{"scheme":"exact","network":"gno:onyx-1","asset":"ugnot","amount":"250000","payTo":"g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq","maxTimeoutSeconds":300}]}`
		w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString([]byte(required)))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer server.Close()

	resp, err := runFetchTool(t, server.Client(), server.URL)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Request failed with status code: 402 Payment Required")
	require.Contains(t, resp.Content, "The resource charges through x402: 250000ugnot on gno:onyx-1.")
	require.Contains(t, resp.Content, X402FetchToolName)
}

func TestFetchToolReturnsContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello from fetch"))
	}))
	defer server.Close()

	resp, err := runFetchTool(t, server.Client(), server.URL)
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "hello from fetch")
}

// fetchServer serves body as contentType ("" for none, unsniffed), with
// its Content-Length.
func fetchServer(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType == "" {
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An agent on the AI box fetched a song, a video and a model file, and
// was told only that their content was "not valid UTF-8". A response that
// is not text now says what it is, and to save it with download; one of
// a type that is never text is not read at all. Text sent as
// octet-stream, SVG, and a text page in another encoding are as before.
func TestFetchSaysAFileIsNotText(t *testing.T) {
	t.Parallel()

	binary := []byte{0xff, 0xfe, 0x00, 0x01, 0x80, 0x81}
	song := append([]byte("ID3"), make([]byte, 4_200_000)...)
	for _, c := range []struct {
		contentType string
		body        []byte
		want        string
		isErr       bool
	}{
		{"audio/mpeg", song, "this is not a text page but audio/mpeg of 4.2 MB; fetching reads text. Save it with the download tool", true},
		{"application/octet-stream", binary, "this is not a text page but application/octet-stream; fetching reads text. Save it with the download tool", true},
		{"", binary, "this is not a text page but a file of unknown type; fetching reads text. Save it with the download tool", true},
		{"text/html; charset=iso-8859-1", []byte("<p>caf\xe9</p>"), "response content is not valid UTF-8", true},
		{"application/octet-stream", []byte("plain words"), "plain words", false},
		{"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "<svg", false},
	} {
		srv := fetchServer(t, c.contentType, c.body)
		resp, err := runFetchTool(t, srv.Client(), srv.URL)
		require.NoError(t, err)
		require.Equal(t, c.isErr, resp.IsError, c.contentType)
		if c.isErr {
			require.Equal(t, c.want, resp.Content, c.contentType)
		} else {
			require.Contains(t, resp.Content, c.want, c.contentType)
		}
	}
}

// web_fetch, through FetchURLAndConvert, says the same.
func TestFetchURLAndConvertSaysAFileIsNotText(t *testing.T) {
	t.Parallel()

	srv := fetchServer(t, "video/mp4", make([]byte, 2048))
	_, err := FetchURLAndConvert(context.Background(), srv.Client(), srv.URL)
	require.EqualError(t, err, "this is not a text page but video/mp4 of 2.0 kB; fetching reads text. Save it with the download tool")

	srv = fetchServer(t, "application/octet-stream", []byte{0xff, 0xfe, 0x00})
	_, err = FetchURLAndConvert(context.Background(), srv.Client(), srv.URL)
	require.EqualError(t, err, "this is not a text page but application/octet-stream of 3 B; fetching reads text. Save it with the download tool")

	srv = fetchServer(t, "text/plain; charset=windows-1252", []byte("caf\xe9"))
	_, err = FetchURLAndConvert(context.Background(), srv.Client(), srv.URL)
	require.EqualError(t, err, "response content is not valid UTF-8")
}
