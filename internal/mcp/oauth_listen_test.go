package mcp

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The browser that brings the sign-in's code back gets its answer whole,
// though the sign-in stops the listener as soon as the code arrives: the
// code goes to the sign-in before the page is written, and a listener
// closed under it left the tab with an error (seen once in CI, under load;
// on a quiet machine the stop seldom lands in that moment, so this guards
// the answer more than it finds the race).
func TestListenAnswersTheBrowserBeforeItStops(t *testing.T) {
	for i := 0; i < 50; i++ {
		redirect, back, stop, err := Listen(0, func() string { return "st" })
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			<-back
			stop()
		}()
		resp, err := http.Get(redirect + "?code=c&state=st")
		if err != nil {
			t.Fatalf("try %d: the browser could not come back: %v", i, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || !strings.Contains(string(body), "Signed in") {
			t.Fatalf("try %d: the browser read %q, %v", i, body, err)
		}
	}
}
