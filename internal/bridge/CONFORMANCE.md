# How `gnosh bridge` conforms to `spec/gnosh/bridge.md`

Each rule of the spec beside the code that keeps it and the test that
proves it. **Spec** is a section of `bridge.md`. **Code** is in this
package unless it names `memjoin` (`internal/memjoin/bridge.go`), which
joins the protocol to the machines. Tests named `TestSpec…` are in
`server_test.go` and use a tool made for the test; `TestBridgePushWorkPop`
(`internal/memjoin/bridge_test.go`) runs the whole program, `gnosh
bridge`, with the real machine tools and mem, the GnoVM in the test's
process.

| Spec | Rule | Code | Test |
| :- | :- | :- | :- |
| 2 | `gnosh bridge [--cwd]`; the folder's project; no model, provider or config needed | `cli` table row `bridge`; `memjoin` `bridgeCommand` | `TestBridgePushWorkPop` (no provider is asked) |
| 2 | ends when standard input ends; its machines go with it | `Serve` returns nil at EOF; `bridgeCommand` defers `p.shutdown` | `TestBridgePushWorkPop` (exit 0 after close) |
| 3 | one JSON object a line each way, up to `MaxLine` | `Serve` | every test |
| 3 | answered one at a time, in order; `id` echoed | `Serve`, `Handle` | `TestSpec3And8Errors` |
| 4 | hello first, else `protocol` and the bridge ends | `Handle` | `TestSpec4Hello` |
| 4 | one version; a host's other version gets `protocol` with `protocols` | `helloReply` | `TestSpec4Hello` |
| 4 | reply: protocol, build, version, project, ops | `helloReply`; `memjoin` passes `buildinfo.BuildID` | `TestSpec4Hello`, `TestBridgePushWorkPop` |
| 5 | gnosh's machine tools, names, descriptions and schemas unchanged, in a fixed order; `confirm` | `toolsReply`; `memjoin` `gnovmtools.New(...).Tools()` | `TestSpec5And6Calls`, `TestBridgePushWorkPop` |
| 6.1 | `session`, `call_id`, `name` needed; empty input is `{}` | `call` | `TestSpec3And8Errors`, `TestSpec5And6Calls` |
| 6.1 | a failure the model sees is `ok` with `is_error` | `call` | `TestSpec5And6Calls` |
| 6.1 | `frames` of the call's own session only | `call`; `memjoin` `bridgeFrames` | `TestSpec5And6Calls`, `TestBridgePushWorkPop` |
| 6.2 | a call `core.Action.Confirm` says needs a person runs only with `confirmed` | `call` | `TestSpec5And6Calls`, `TestBridgePushWorkPop` (`gnovm_drop` delete) |
| 6.3 | a session's stack: its last view's, with later calls' frames applied | `Server.Stack`, `apply`, `view` | `TestSpec5And6Calls`, `TestSpec7View`, `TestBridgePushWorkPop` (the pop is made from it) |
| 6.3 | `MemFresh` is not applied in version 1 | `memjoin` leaves `MemFresh` nil | — (stated) |
| 7.1 | entries need unique uuids | `view` | `TestSpec3And8Errors` |
| 7.3 | `messages`, `sources`, `stack`, `tokens`, `over_budget`, `new_slides` from `transcript.View` with `Trace` | `view` | `TestSpec7View`, `TestBridgePushWorkPop` |
| 7.3 | after a pop the popped work is gone and the push's result holds the note | `transcript.View` | `TestBridgePushWorkPop` |
| 7.4 | the prefix through the push is byte for byte the same; a later request extends it | `transcript.View` | `TestBridgePushWorkPop` |
| 8 | the error codes; the bridge goes on after an error but `protocol` | `fail`, `Handle` | `TestSpec3And8Errors`, `TestSpec4Hello` |

Not yet proved by a test of this package: slides under a `budget`
(`new_slides`; the rule itself is `transcript`'s and tested there), a line
longer than `MaxLine`, and the `unavailable` code (a machine that cannot
start is today a result the model sees, from `gnovm/tools`).
