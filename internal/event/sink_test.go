package event

import "testing"

// Init with an empty endpoint must leave the client nil, so send() is a no-op
// and nothing is ever sent — in particular not to Charm, which is no longer a
// default anywhere.
func TestInitEmptyEndpointSendsNowhere(t *testing.T) {
	client = nil
	Init("", "")
	if client != nil {
		t.Fatal("an empty metrics endpoint must leave the client nil")
	}
	send("app initialized") // must not panic with a nil client
}
