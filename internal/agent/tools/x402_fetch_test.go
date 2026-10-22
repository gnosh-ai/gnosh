package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/x402gno"
	"github.com/gnolang/gno/tm2/pkg/amino"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/keys"
	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/require"
	"github.com/x402-foundation/x402/go/v2/types"
)

const (
	// x402Mnemonic is gno's public test1 key, the buyer. Nothing is broadcast.
	x402Mnemonic = "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast"
	x402Buyer    = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5"
	x402PayTo    = "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq"

	// x402Session is a session key's address, and x402SessionMnemonic its key.
	x402SessionMnemonic = "dance carry grunt uphold cloth wire usage usage clock reduce surprise page destroy reopen multiply away defense promote person web cigar immense armed enough"
)

// x402Key is a key held in memory that signs like gnokey.
type x402Key struct {
	kb  keys.Keybase
	pub crypto.PubKey
}

func newX402Key(t *testing.T, mnemonic string) x402Key {
	t.Helper()
	kb := keys.NewInMemory()
	info, err := kb.CreateAccount("k", mnemonic, "", "", 0, 0)
	require.NoError(t, err)
	return x402Key{kb: kb, pub: info.GetPubKey()}
}

// listLine is how gnokey list shows the key under name.
func (k x402Key) listLine(name string) string {
	return fmt.Sprintf("0. %s (local) - addr: %s pub: %s, path: <nil>", name, k.pub.Address(), crypto.PubKeyToBech32(k.pub))
}

// signatureFile is what gnokey sign -output-document writes when this key
// signs signBytes.
func (k x402Key) signatureFile(t *testing.T, signBytes []byte) string {
	t.Helper()
	sig, pub, err := k.kb.Sign("k", "", signBytes)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "signature.json")
	require.NoError(t, os.WriteFile(path, amino.MustMarshalJSON(std.Signature{PubKey: pub, Signature: sig}), 0o600))
	return path
}

// x402Gnokey is a gnokey on PATH that lists GNOKEY_FAKE_LIST and, to sign,
// records its standard input and copies GNOKEY_FAKE_SIGNATURE to the
// -output-document it is given, or fails the way a wrong password does when
// GNOKEY_FAKE_SIGN_FAIL is set. Every invocation's arguments are appended to
// the returned file, one per line, each run ending in "--".
type x402Gnokey struct {
	argsFile, stdinFile string
}

func fakeX402Gnokey(t *testing.T, list string) x402Gnokey {
	t.Helper()
	dir := t.TempDir()
	g := x402Gnokey{argsFile: filepath.Join(dir, "args"), stdinFile: filepath.Join(dir, "stdin")}
	script := `#!/bin/sh
printf '%s\n' "$@" -- >> "$GNOKEY_FAKE_ARGS"
case "$1" in
list) printf '%s\n' "$GNOKEY_FAKE_LIST" ;;
sign)
  cat > "$GNOKEY_FAKE_STDIN"
  if [ -n "$GNOKEY_FAKE_SIGN_FAIL" ]; then echo "unable to sign transaction, invalid account password"; exit 1; fi
  out=""; prev=""
  for a in "$@"; do
    if [ "$prev" = "-output-document" ]; then out="$a"; fi
    prev="$a"
  done
  cp "$GNOKEY_FAKE_SIGNATURE" "$out"
  echo "Signature generated and successfully saved to $out"
  ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gnokey"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GNOKEY_FAKE_ARGS", g.argsFile)
	t.Setenv("GNOKEY_FAKE_STDIN", g.stdinFile)
	t.Setenv("GNOKEY_FAKE_LIST", list)
	t.Setenv("GNOKEY_FAKE_SIGN_FAIL", "")
	return g
}

// runs returns gnokey's invocations, each as its arguments.
func (g x402Gnokey) runs(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(g.argsFile)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var runs [][]string
	var run []string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "--" {
			runs = append(runs, run)
			run = nil
			continue
		}
		run = append(run, line)
	}
	return runs
}

// stubX402Chain is a gno.land node's JSON-RPC endpoint for chain dev: onyx-1's
// gas price, test1's account at number 12 and sequence 7, and the session
// key's session under test1 at 34 and 3.
func stubX402Chain(t *testing.T) *httptest.Server {
	t.Helper()
	session := newX402Key(t, x402SessionMnemonic).pub.Address().String()
	answers := map[string]string{
		"auth/gasprice":              `{"gas": "1000", "price": "1ugnot"}`,
		"auth/accounts/" + x402Buyer: `{"BaseAccount": {"address": "` + x402Buyer + `", "coins": "", "public_key": null, "account_number": "12", "sequence": "7"}}`,
		"auth/accounts/" + x402Buyer + "/session/" + session: `{"BaseSessionAccount": {"BaseAccount": {"address": "` + session +
			`", "coins": "", "public_key": null, "account_number": "34", "sequence": "3"}}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"","result":{"node_info":{"network":"dev"}}}`)
		case "/abci_query":
			path, _ := strconv.Unquote(r.URL.Query().Get("path"))
			data := "null"
			if answer, ok := answers[path]; ok {
				data = strconv.Quote(base64.StdEncoding.EncodeToString([]byte(answer)))
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"","result":{"response":{"ResponseBase":{"Error":null,"Data":%s,"Log":""}}}}`, data)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// x402Offer is 250000ugnot to x402PayTo on gno:dev.
func x402Offer() types.PaymentRequirements {
	return types.PaymentRequirements{
		Scheme:            "exact",
		Network:           "gno:dev",
		Asset:             "ugnot",
		Amount:            "250000",
		PayTo:             x402PayTo,
		MaxTimeoutSeconds: 300,
		Extra:             map[string]any{"areFeesSponsored": false},
	}
}

// stubSeller sells the weather for offer. An unpaid request gets 402 with the
// offer in PAYMENT-REQUIRED. A paid one is checked the way a facilitator
// checks it — the offer it accepted, one bank send of exactly the price, and
// buyer's signature over the sign doc for chain dev at the account state the
// stub chain reports — and answered by paid, which gets the decoded payment.
type stubSeller struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	payments []std.Tx
}

func newStubSeller(t *testing.T, offer types.PaymentRequirements, buyer crypto.PubKey, number, sequence uint64, paid func(w http.ResponseWriter, tx std.Tx)) *stubSeller {
	t.Helper()
	s := &stubSeller{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		s.mu.Unlock()
		header := r.Header.Get("PAYMENT-SIGNATURE")
		if header == "" {
			required, err := json.Marshal(types.PaymentRequired{
				X402Version: 2,
				Error:       "Payment required",
				Resource:    &types.ResourceInfo{URL: "http://" + r.Host + r.URL.Path, Description: "Weather data"},
				Accepts:     []types.PaymentRequirements{offer},
			})
			require.NoError(t, err)
			w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(required))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(header)
		require.NoError(t, err)
		var payload types.PaymentPayload
		require.NoError(t, json.Unmarshal(raw, &payload))
		require.Equal(t, 2, payload.X402Version)
		require.Equal(t, offer.PayTo, payload.Accepted.PayTo)
		require.Equal(t, offer.Amount, payload.Accepted.Amount)
		transaction, err := base64.StdEncoding.DecodeString(payload.Payload["transaction"].(string))
		require.NoError(t, err)
		var tx std.Tx
		require.NoError(t, amino.Unmarshal(transaction, &tx))
		require.Len(t, tx.Msgs, 1)
		send := tx.Msgs[0].(bank.MsgSend)
		require.Equal(t, offer.PayTo, send.ToAddress.String())
		require.Equal(t, offer.Amount+offer.Asset, send.Amount.String())
		require.Len(t, tx.Signatures, 1)
		rendering, err := std.VerifySignaturePayload(buyer, tx.SignDoc("dev", number, sequence), tx.Signatures[0].Signature)
		require.NoError(t, err)
		require.NotEqual(t, std.PayloadRenderingNone, rendering, "the seller got a payment whose signature does not verify")
		s.mu.Lock()
		s.payments = append(s.payments, tx)
		s.mu.Unlock()
		paid(w, tx)
	}))
	t.Cleanup(s.Close)
	return s
}

// served answers a paid request with the weather and a settlement receipt.
func served(w http.ResponseWriter, tx std.Tx) {
	receipt, _ := json.Marshal(map[string]any{
		"success": true, "transaction": "A1B2C3", "network": "gno:dev",
		"payer": tx.Msgs[0].(bank.MsgSend).FromAddress.String(),
	})
	w.Header().Set("PAYMENT-RESPONSE", base64.StdEncoding.EncodeToString(receipt))
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"forecast":"sunny"}`)
}

// signedFor is the signature file gnokey would write for key paying offer
// against the stub chain, with master set when key is a session of master.
func signedFor(t *testing.T, chain *httptest.Server, key x402Key, master crypto.Address, offer types.PaymentRequirements) string {
	t.Helper()
	node, err := x402gno.NewRPCNode(chain.URL)
	require.NoError(t, err)
	payment, err := x402gno.Prepare(context.Background(), node, key.pub, master, offer, "")
	require.NoError(t, err)
	signBytes, err := payment.SignBytes()
	require.NoError(t, err)
	return key.signatureFile(t, signBytes)
}

func runX402Fetch(t *testing.T, tool fantasy.AgentTool, params X402FetchParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(gnokeyContext(), fantasy.ToolCall{ID: "call-1", Name: X402FetchToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

// pendingQuestion waits for the tool to ask and returns what it asks.
func pendingQuestion(t *testing.T, questions question.Service) question.Question {
	t.Helper()
	var pending question.Request
	require.Eventually(t, func() bool {
		var ok bool
		pending, ok = questions.Pending()
		return ok
	}, 5*time.Second, 10*time.Millisecond, "the tool must ask for the password")
	require.Len(t, pending.Questions, 1)
	return pending.Questions[0]
}

// TestX402Fetch_PaysWhatTheUserApproved is the whole purchase: the prompt
// names the price, payee, chain and fee; the password goes to gnokey's
// standard input with the payment's chain, account number and sequence; the
// seller gets a payment its signature check accepts; and the agent gets the
// receipt and the weather, never the password.
func TestX402Fetch_PaysWhatTheUserApproved(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	gnokey := fakeX402Gnokey(t, buyer.listLine("buyer"))
	chain := stubX402Chain(t)
	t.Setenv("GNOKEY_FAKE_SIGNATURE", signedFor(t, chain, buyer, crypto.Address{}, x402Offer()))
	seller := newStubSeller(t, x402Offer(), buyer.pub, 12, 7, served)
	questions := question.NewService()
	tool := NewX402FetchTool(allowingPermissions(), questions, t.TempDir(), nil)

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runX402Fetch(t, tool, X402FetchParams{URL: seller.URL + "/weather", Key: "buyer", Remote: chain.URL, Description: "Buy the weather"})
	}()
	q := pendingQuestion(t, questions)
	require.True(t, q.Secret, "the password prompt must be masked")
	require.Equal(t, "Password for key buyer to pay", q.Text)
	require.Contains(t, q.Description, "**Pays 0.25 GNOT (250000ugnot)** to `"+x402PayTo+"` on **dev**")
	require.Contains(t, q.Description, "a network fee of 0.02 GNOT (20000ugnot)")
	require.Contains(t, q.Description, seller.URL+"/weather")
	require.True(t, questions.Answer([]question.Answer{{QuestionID: q.ID, FillInText: "hunter2 pass"}}))

	resp := <-done
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "Paid 0.25 GNOT (250000ugnot) to "+x402PayTo+" on dev, plus a 0.02 GNOT (20000ugnot) network fee.")
	require.Contains(t, resp.Content, "Transaction A1B2C3, payer "+x402Buyer)
	require.Contains(t, resp.Content, "sunny")
	require.NotContains(t, resp.Content, "hunter2")
	require.Len(t, seller.payments, 1)

	runs := gnokey.runs(t)
	require.Len(t, runs, 2, "gnokey lists the key, then signs once")
	require.Equal(t, []string{"list"}, runs[0])
	sign := runs[1]
	require.Equal(t, []string{"sign", passwordStdinFlag, "-quiet"}, sign[:3])
	require.Equal(t, "buyer", sign[len(sign)-1])
	joined := strings.Join(sign, " ")
	require.Contains(t, joined, "-chainid dev -account-number 12 -account-sequence 7")
	stdin, err := os.ReadFile(gnokey.stdinFile)
	require.NoError(t, err)
	require.Equal(t, "hunter2 pass\n", string(stdin))
}

// TestX402Fetch_ASessionKeyPaysForItsMaster: the session's own account state
// is what gnokey signs over, and the payment the seller gets names the session
// and moves the master's coins.
func TestX402Fetch_ASessionKeyPaysForItsMaster(t *testing.T) {
	session := newX402Key(t, x402SessionMnemonic)
	master := crypto.MustAddressFromString(x402Buyer)
	gnokey := fakeX402Gnokey(t, session.listLine("agent"))
	chain := stubX402Chain(t)
	t.Setenv("GNOKEY_FAKE_SIGNATURE", signedFor(t, chain, session, master, x402Offer()))
	seller := newStubSeller(t, x402Offer(), session.pub, 34, 3, served)
	questions := question.NewService()
	tool := NewX402FetchTool(allowingPermissions(), questions, t.TempDir(), nil)

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "agent", Master: x402Buyer, Remote: chain.URL})
	}()
	q := pendingQuestion(t, questions)
	require.Contains(t, q.Description, "from "+x402Buyer+", signed by its session key agent")
	require.True(t, questions.Answer([]question.Answer{{QuestionID: q.ID, FillInText: "pw"}}))

	resp := <-done
	require.False(t, resp.IsError, resp.Content)
	require.Len(t, seller.payments, 1)
	paid := seller.payments[0]
	require.Equal(t, master, paid.Msgs[0].(bank.MsgSend).FromAddress)
	require.Equal(t, session.pub.Address(), paid.Signatures[0].SessionAddr)
	require.Contains(t, strings.Join(gnokey.runs(t)[1], " "), "-account-number 34 -account-sequence 3")
}

// TestX402Fetch_DeclinedPromptPaysNothing: Esc at the password prompt ends the
// turn, and nothing is signed or sent.
func TestX402Fetch_DeclinedPromptPaysNothing(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	gnokey := fakeX402Gnokey(t, buyer.listLine("buyer"))
	chain := stubX402Chain(t)
	seller := newStubSeller(t, x402Offer(), buyer.pub, 12, 7, served)
	questions := question.NewService()
	tool := NewX402FetchTool(allowingPermissions(), questions, t.TempDir(), nil)

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "buyer", Remote: chain.URL})
	}()
	pendingQuestion(t, questions)
	require.True(t, questions.Cancel())

	resp := <-done
	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn, "a declined payment ends the turn so the user is asked")
	require.Contains(t, resp.Content, "declined")
	require.Len(t, gnokey.runs(t), 1, "gnokey only listed the key; it never signed")
	require.Empty(t, seller.payments)
	require.Equal(t, 1, seller.requests, "only the unpaid request reached the seller")
}

// TestX402Fetch_PermissionDeniedTouchesNothing: refusing the tool sends no
// request anywhere and runs no gnokey.
func TestX402Fetch_PermissionDeniedTouchesNothing(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	gnokey := fakeX402Gnokey(t, buyer.listLine("buyer"))
	seller := newStubSeller(t, x402Offer(), buyer.pub, 12, 7, served)
	permissions := &denyingPermissionService{mockBashPermissionService: &mockBashPermissionService{}}
	tool := NewX402FetchTool(permissions, question.NewService(), t.TempDir(), nil)

	resp := runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "buyer", Master: x402Buyer})

	require.True(t, resp.IsError)
	require.Equal(t, X402FetchToolName, permissions.request.ToolName)
	require.Equal(t, X402FetchPermissionsParams{URL: seller.URL, Key: "buyer", Master: x402Buyer}, permissions.request.Params)
	require.Zero(t, seller.requests)
	require.Empty(t, gnokey.runs(t))
}

// TestX402Fetch_OffersItWillNotPayAreRefusedUnasked: mainnet, a price over
// the cap, another denomination and a foreign chain are refused before the
// user is asked for anything, with the reason.
func TestX402Fetch_OffersItWillNotPayAreRefusedUnasked(t *testing.T) {
	cases := map[string]struct {
		mutate func(*types.PaymentRequirements)
		want   string
	}{
		"mainnet":          {func(o *types.PaymentRequirements) { o.Network = "gno:gnoland-1" }, "never mainnet"},
		"an unknown chain": {func(o *types.PaymentRequirements) { o.Network = "gno:somechain" }, "only on dev and test chains"},
		"over the cap":     {func(o *types.PaymentRequirements) { o.Amount = "1000001" }, "more than the 1000000 ugnot (1 GNOT)"},
		"another denom":    {func(o *types.PaymentRequirements) { o.Asset = "uother" }, "only in ugnot"},
		"another scheme":   {func(o *types.PaymentRequirements) { o.Scheme = "upto" }, "only the exact scheme"},
		"not gno at all":   {func(o *types.PaymentRequirements) { o.Network = "eip155:8453" }, "not a gno.land chain"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			buyer := newX402Key(t, x402Mnemonic)
			gnokey := fakeX402Gnokey(t, buyer.listLine("buyer"))
			offer := x402Offer()
			tc.mutate(&offer)
			seller := newStubSeller(t, offer, buyer.pub, 12, 7, served)
			questions := question.NewService()
			tool := NewX402FetchTool(allowingPermissions(), questions, t.TempDir(), nil)

			resp := runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "buyer", Remote: "127.0.0.1:1"})

			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, tc.want)
			_, asked := questions.Pending()
			require.False(t, asked)
			require.Len(t, gnokey.runs(t), 1, "gnokey only listed the key")
			require.Empty(t, seller.payments)
		})
	}
}

// TestX402Fetch_AFreeResourceIsNotPaid: a seller that does not answer 402 is
// fetched like any page, and nothing is asked or signed.
func TestX402Fetch_AFreeResourceIsNotPaid(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	gnokey := fakeX402Gnokey(t, buyer.listLine("buyer"))
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "free weather: sunny")
	}))
	t.Cleanup(free.Close)
	tool := NewX402FetchTool(allowingPermissions(), question.NewService(), t.TempDir(), nil)

	resp := runX402Fetch(t, tool, X402FetchParams{URL: free.URL, Key: "buyer"})

	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "nothing was paid")
	require.Contains(t, resp.Content, "free weather: sunny")
	require.Len(t, gnokey.runs(t), 1)
}

// TestX402Fetch_AWrongPasswordPaysNothing: gnokey's refusal is reported, with
// the password scrubbed, and the seller gets no payment.
func TestX402Fetch_AWrongPasswordPaysNothing(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	fakeX402Gnokey(t, buyer.listLine("buyer"))
	t.Setenv("GNOKEY_FAKE_SIGN_FAIL", "1")
	chain := stubX402Chain(t)
	seller := newStubSeller(t, x402Offer(), buyer.pub, 12, 7, served)
	questions := question.NewService()
	tool := NewX402FetchTool(allowingPermissions(), questions, t.TempDir(), nil)

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "buyer", Remote: chain.URL})
	}()
	q := pendingQuestion(t, questions)
	require.True(t, questions.Answer([]question.Answer{{QuestionID: q.ID, FillInText: "wrong"}}))

	resp := <-done
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "invalid account password")
	require.Empty(t, seller.payments)
}

// TestX402Fetch_ASignatureOverAnythingElseIsNotSent: a signature gnokey made
// over something other than this payment never reaches the seller.
func TestX402Fetch_ASignatureOverAnythingElseIsNotSent(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	fakeX402Gnokey(t, buyer.listLine("buyer"))
	chain := stubX402Chain(t)
	t.Setenv("GNOKEY_FAKE_SIGNATURE", buyer.signatureFile(t, []byte("some other document")))
	seller := newStubSeller(t, x402Offer(), buyer.pub, 12, 7, served)
	questions := question.NewService()
	tool := NewX402FetchTool(allowingPermissions(), questions, t.TempDir(), nil)

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "buyer", Remote: chain.URL})
	}()
	q := pendingQuestion(t, questions)
	require.True(t, questions.Answer([]question.Answer{{QuestionID: q.ID, FillInText: "pw"}}))

	resp := <-done
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "the signature does not verify")
	require.Empty(t, seller.payments)
	require.Equal(t, 1, seller.requests)
}

// TestX402Fetch_ARefusedPaymentSaysWhy: the seller's facilitator refuses the
// payment, and the agent gets its reason rather than the resource.
func TestX402Fetch_ARefusedPaymentSaysWhy(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	fakeX402Gnokey(t, buyer.listLine("buyer"))
	chain := stubX402Chain(t)
	t.Setenv("GNOKEY_FAKE_SIGNATURE", signedFor(t, chain, buyer, crypto.Address{}, x402Offer()))
	seller := newStubSeller(t, x402Offer(), buyer.pub, 12, 7, func(w http.ResponseWriter, _ std.Tx) {
		challenge, _ := json.Marshal(types.PaymentRequired{
			X402Version: 2,
			Error:       "invalid_exact_gno_transaction_simulation_failed",
			Accepts:     []types.PaymentRequirements{x402Offer()},
		})
		w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(challenge))
		w.WriteHeader(http.StatusPaymentRequired)
	})
	questions := question.NewService()
	tool := NewX402FetchTool(allowingPermissions(), questions, t.TempDir(), nil)

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "buyer", Remote: chain.URL})
	}()
	q := pendingQuestion(t, questions)
	require.True(t, questions.Answer([]question.Answer{{QuestionID: q.ID, FillInText: "pw"}}))

	resp := <-done
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Not paid: the seller refused the payment (invalid_exact_gno_transaction_simulation_failed)")
	require.NotContains(t, resp.Content, "sunny")
}

// TestX402Fetch_AKeyGnokeyDoesNotHoldIsNamed: the agent hears that the key is
// missing and that keys are added in a terminal, before anything is fetched.
func TestX402Fetch_AKeyGnokeyDoesNotHoldIsNamed(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	fakeX402Gnokey(t, buyer.listLine("someone-else"))
	seller := newStubSeller(t, x402Offer(), buyer.pub, 12, 7, served)
	tool := NewX402FetchTool(allowingPermissions(), question.NewService(), t.TempDir(), nil)

	resp := runX402Fetch(t, tool, X402FetchParams{URL: seller.URL, Key: "buyer", Home: "/keys"})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, `gnokey's keybase at /keys holds no key "buyer"`)
	require.Zero(t, seller.requests)
}

// TestX402Fetch_PaysNothingWithNoOneToAsk: without a user to type the
// password there is no payment.
func TestX402Fetch_PaysNothingWithNoOneToAsk(t *testing.T) {
	tool := NewX402FetchTool(allowingPermissions(), nil, t.TempDir(), nil)

	resp := runX402Fetch(t, tool, X402FetchParams{URL: "https://example.com", Key: "buyer"})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "this run has no one to ask")
}

// TestGnokeyKeyIsReadFromGnokeyList parses gnokey's own listing, by name or
// by address, and refuses keys that cannot sign or do not add up.
func TestGnokeyKeyIsReadFromGnokeyList(t *testing.T) {
	buyer := newX402Key(t, x402Mnemonic)
	other := newX402Key(t, x402SessionMnemonic)
	list := strings.Join([]string{
		buyer.listLine("my key"),
		strings.Replace(other.listLine("cold"), "(local)", "(offline)", 1),
		fmt.Sprintf("2. liar (local) - addr: %s pub: %s, path: <nil>", buyer.pub.Address(), crypto.PubKeyToBech32(other.pub)),
	}, "\n")
	fakeX402Gnokey(t, list)
	ctx := context.Background()

	byName, err := gnokeyKey(ctx, "", "my key")
	require.NoError(t, err)
	require.Equal(t, "my key", byName.name)
	require.Equal(t, buyer.pub, byName.pub)

	byAddress, err := gnokeyKey(ctx, "", x402Buyer)
	require.NoError(t, err)
	require.Equal(t, "my key", byAddress.name)

	_, err = gnokeyKey(ctx, "", "cold")
	require.ErrorContains(t, err, "offline key, which cannot sign")

	_, err = gnokeyKey(ctx, "", "liar")
	require.ErrorContains(t, err, "not its address's")
}

// TestGnotAmount shows GNOT beside the exact coin.
func TestGnotAmount(t *testing.T) {
	cases := map[int64]string{
		250000:  "0.25 GNOT (250000ugnot)",
		20000:   "0.02 GNOT (20000ugnot)",
		1000000: "1 GNOT (1000000ugnot)",
		1:       "0.000001 GNOT (1ugnot)",
	}
	for amount, want := range cases {
		require.Equal(t, want, gnotAmount(std.Coin{Denom: "ugnot", Amount: amount}))
	}
	require.Equal(t, "5uother", gnotAmount(std.Coin{Denom: "uother", Amount: 5}))
}

// TestX402ChainAllowed pins which chains crash pays on: local and test
// chains, and nothing else.
func TestX402ChainAllowed(t *testing.T) {
	for _, chain := range []string{"dev", "staging", "onyx-1", "onyx-2", "test12"} {
		require.True(t, x402ChainAllowed(chain), chain)
	}
	for _, chain := range []string{"gnoland-1", "gnoland", "mainnet", "", "portal-loop", "tendermint_test"} {
		require.False(t, x402ChainAllowed(chain), chain)
	}
}
