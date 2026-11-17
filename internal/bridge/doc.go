// Package bridge is `gnosh bridge`: gnosh's machine tools and its frames
// view, offered to another harness as JSON lines on standard input and
// output. spec/gnosh/bridge.md is the protocol; CONFORMANCE.md ties each of
// its rules to the code here and the test that holds it.
//
// The package knows nothing of the GnoVM. The join (internal/memjoin)
// hands it the tools, the way to read a call's frames, and the build it
// reports; the view is transcript.View. What the bridge keeps between
// requests is one thing per session: the stack that session's mem calls
// are made from (bridge.md §6.3).
package bridge
