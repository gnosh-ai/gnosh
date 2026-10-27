package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/x402gno"
	"github.com/gnolang/gno/tm2/pkg/amino"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/gnolang/x402/client/exact"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	"github.com/x402-foundation/x402/go/v2/types"
)

const X402FetchToolName = "x402_fetch"

//go:embed x402_fetch.md
var x402FetchDescription string

// X402FetchParams are the x402_fetch tool's inputs.
type X402FetchParams struct {
	URL         string `json:"url" description:"The URL to fetch; it answers 402 Payment Required with terms to pay on a gno.land chain"`
	Key         string `json:"key" description:"The gnokey key name or address to pay from, as the user named it"`
	Master      string `json:"master,omitempty" description:"Only when key is a session key: the address of the account that registered it, which pays"`
	Remote      string `json:"remote,omitempty" description:"RPC address of a node on the chain the seller names, as gnokey's -remote takes it (default: that chain's public RPC, or 127.0.0.1:26657 for dev)"`
	Home        string `json:"home,omitempty" description:"gnokey's home directory holding the key, as gnokey's -home takes it (default: gnokey's own)"`
	Format      string `json:"format,omitempty" description:"The format to return the content in: text, markdown or html (default text)"`
	Description string `json:"description" description:"One line telling the user what is being bought and why"`
}

// X402FetchPermissionsParams are what the permission dialog shows for an
// x402_fetch call: the resource and the key that would pay for it. The price
// is not known until the seller names it, so the password prompt shows that.
type X402FetchPermissionsParams struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	Master      string `json:"master,omitempty"`
	Description string `json:"description,omitempty"`
}

// x402MaxPrice is the most one payment may cost, in ugnot: 1 GNOT. The user
// approves each price at the password prompt; this keeps a reflexive yes from
// paying anything larger. The network fee is paid on top.
const x402MaxPrice = 1_000_000

// x402Asset is the one denomination klash pays in, gno.land's own coin.
const x402Asset = "ugnot"

// x402SignTimeout bounds gnokey's signing, which reads no network but may wait
// on a Ledger's confirmation.
const x402SignTimeout = 2 * time.Minute

// x402Remotes are the public RPC endpoints of the chains klash pays on, for a
// call that names no remote.
var x402Remotes = map[string]string{
	"dev":     "127.0.0.1:26657",
	"staging": "https://rpc.staging.gno.land:443",
	"onyx-1":  "https://rpc.onyx.testnets.gno.land:443",
}

// x402ChainAllowed reports whether klash pays on chain: a local dev chain or
// a test network. x402 on gno.land is unaudited and meant for testnets only,
// so mainnet, and any chain not known to be a test chain, is refused before a
// payment exists, the way gno-mcp confines its writes.
func x402ChainAllowed(chain string) bool {
	return chain == "dev" || chain == "staging" ||
		strings.HasPrefix(chain, "test") || strings.HasPrefix(chain, "onyx-")
}

// NewX402FetchTool makes the tool that fetches a URL and, when the seller
// answers 402 Payment Required with x402 terms on a gno.land test chain, pays
// for it with a key gnokey keeps.
//
// Every payment is the user's: the permission dialog names the resource and
// the key, and the password prompt names the price, the payee and the fee.
// The password goes to gnokey's standard input and nowhere else, gnokey signs
// one bank send of exactly the price, and klash checks the signature before it
// hands the payment to the seller. questions is nil when there is no user to
// ask, as in a non-interactive run; the tool then pays nothing.
func NewX402FetchTool(permissions permission.Service, questions question.Service, workingDir string, client *http.Client) fantasy.AgentTool {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return fantasy.NewAgentTool(
		X402FetchToolName,
		x402FetchDescription,
		func(ctx context.Context, params X402FetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
				return fantasy.NewTextErrorResponse("url must start with http:// or https://"), nil
			}
			if strings.TrimSpace(params.Key) == "" {
				return fantasy.NewTextErrorResponse("key is required: the gnokey key the user wants to pay from"), nil
			}
			format := strings.ToLower(cmp.Or(params.Format, "text"))
			if format != "text" && format != "markdown" && format != "html" {
				return fantasy.NewTextErrorResponse("format must be one of: text, markdown, html"), nil
			}
			var master crypto.Address
			if params.Master != "" {
				m, err := crypto.AddressFromBech32(params.Master)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("master %q is not a gno address", params.Master)), nil
				}
				master = m
			}
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session ID is required for paying with x402")
			}

			granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        workingDir,
				ToolCallID:  call.ID,
				ToolName:    X402FetchToolName,
				Action:      "pay",
				Description: cmp.Or(params.Description, fmt.Sprintf("Fetch %s, paying from key %s if it charges", params.URL, params.Key)),
				Params:      X402FetchPermissionsParams{URL: params.URL, Key: params.Key, Master: params.Master, Description: params.Description},
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !granted {
				return NewPermissionDeniedResponse(), nil
			}
			if questions == nil {
				return fantasy.NewTextErrorResponse("paying needs the key's password typed by the user, and this run has no one to ask; tell the user the URL charges for access instead"), nil
			}
			if _, err := exec.LookPath("gnokey"); err != nil {
				return fantasy.NewTextErrorResponse("gnokey is not installed or not on PATH, and it signs the payment; see https://docs.gno.land/builders/install"), nil
			}

			signer, err := gnokeyKey(ctx, params.Home, params.Key)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Ask once without paying. Anything but a 402 is the answer.
			unpaid, body, err := x402Get(ctx, client, params.URL, nil)
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to fetch URL: " + err.Error()), nil
			}
			if unpaid.StatusCode != http.StatusPaymentRequired {
				if unpaid.StatusCode != http.StatusOK {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d %s", unpaid.StatusCode, http.StatusText(unpaid.StatusCode))), nil
				}
				content, err := formatFetched(body, unpaid.Header.Get("Content-Type"), format)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				return fantasy.NewTextResponse("The resource did not ask for payment, so nothing was paid.\n\n" + content), nil
			}

			codec := x402http.Newx402HTTPClient(x402.Newx402Client())
			required, err := codec.GetPaymentRequiredResponse(headerMap(unpaid.Header), body)
			if err != nil {
				return fantasy.NewTextErrorResponse("The seller answered 402 Payment Required without x402 terms klash can read: " + err.Error()), nil
			}
			offer, chain, err := chooseGnoOffer(required.Accepts)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			remote := cmp.Or(params.Remote, x402Remotes[chain])
			if remote == "" {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("the seller charges on chain %s, which has no public node klash knows; pass remote, a node on %s", chain, chain)), nil
			}
			node, err := x402gno.NewRPCNode(remote)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Build the payment and check it against the chain before the user is
			// asked anything: an unfunded account or a node on the wrong chain is
			// reported here, and the prompt shows the exact fee.
			payment, err := exact.Prepare(ctx, node, signer.pub, master, offer, strconv.Itoa(x402MaxPrice))
			if err != nil {
				msg := err.Error()
				switch {
				case strings.Contains(msg, "fund it"):
					msg += " (https://faucet.gno.land funds the test networks)"
					if master.IsZero() {
						// A session key has no account of its own: without
						// master it reads as an unfunded one.
						msg += ". If the key is a session key, call again with master, the address of the account that registered it, which pays"
					}
				case errors.Is(err, exact.ErrExcessiveFee):
					// The node, which the call named or klash knows, is the one
					// that reported the price; it may be wrong, or not honest.
					msg += fmt.Sprintf(". The node at %s reported that price; nothing was paid. Tell the user rather than trying another node", node.Endpoint())
				}
				return fantasy.NewTextErrorResponse(msg), nil
			}

			password, err := askX402Password(ctx, questions, sessionID, call.ID, params, signer, payment)
			if errors.Is(err, question.ErrCancelled) {
				resp := fantasy.NewTextErrorResponse("The user declined the payment; nothing was paid. Ask them how to proceed instead of retrying")
				resp.StopTurn = true
				return resp, nil
			}
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			signature, err := gnokeySignPayment(ctx, params.Home, signer.name, password, payment)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			payload, err := payment.Payload(signature)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			envelope, err := json.Marshal(types.PaymentPayload{
				X402Version: 2,
				Payload:     payload.Payload,
				Accepted:    offer,
				Resource:    required.Resource,
				Extensions:  required.Extensions,
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			paymentHeaders, err := codec.EncodePaymentSignatureHeader(envelope)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}

			paid, body, err := x402Get(ctx, client, params.URL, paymentHeaders)
			if err != nil {
				return fantasy.NewTextErrorResponse("The payment was signed but the paid request failed: " + err.Error() + ". Nothing was settled; the signed payment stays valid until the paying account sends another transaction"), nil
			}
			return x402Outcome(codec, paid, body, format, payment), nil
		},
	)
}

// x402Terms describes the x402 offers a 402's PAYMENT-REQUIRED header makes,
// for a fetch that hit one, and how the user can pay: "" when it carries none.
func x402Terms(h http.Header) string {
	if h.Get("PAYMENT-REQUIRED") == "" {
		return ""
	}
	required, err := x402http.Newx402HTTPClient(x402.Newx402Client()).GetPaymentRequiredResponse(headerMap(h), nil)
	if err != nil || len(required.Accepts) == 0 {
		return ""
	}
	offers := make([]string, 0, len(required.Accepts))
	for _, offer := range required.Accepts {
		offers = append(offers, fmt.Sprintf("%s%s on %s", offer.Amount, offer.Asset, offer.Network))
	}
	return "\n\nThe resource charges through x402: " + strings.Join(offers, ", or ") + ". " +
		"If the user wants to pay, " + X402FetchToolName + " pays an offer on a gno.land test chain with a key they name, after they approve the price; it is available in interactive sessions."
}

// x402Get makes one GET for target with headers set, and returns the response
// with its body, read up to MaxFetchSize.
func x402Get(ctx context.Context, client *http.Client, target string, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "crush/1.0")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchSize))
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

// headerMap is h with one value per name, as upstream's codec reads headers.
func headerMap(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for name := range h {
		m[name] = h.Get(name)
	}
	return m
}

// chooseGnoOffer picks the first offer klash pays, and its chain id: the
// exact scheme, on a gno.land chain klash pays on, in ugnot, within
// x402MaxPrice. When none qualifies, the error says why of each.
func chooseGnoOffer(accepts []types.PaymentRequirements) (types.PaymentRequirements, string, error) {
	var why []string
	for _, offer := range accepts {
		label := fmt.Sprintf("%s %s%s on %s", offer.Scheme, offer.Amount, offer.Asset, offer.Network)
		chain, isGno := strings.CutPrefix(offer.Network, "gno:")
		switch {
		case !isGno:
			why = append(why, label+": not a gno.land chain")
		case offer.Scheme != "exact":
			why = append(why, label+": klash pays only the exact scheme")
		case !x402ChainAllowed(chain):
			why = append(why, label+": klash pays only on dev and test chains (dev, staging, onyx-*, test*), never mainnet; x402 on gno.land is unaudited, testnet-only work")
		case offer.Asset != x402Asset:
			why = append(why, label+": klash pays only in ugnot")
		case !withinX402Max(offer.Amount):
			why = append(why, fmt.Sprintf("%s: more than the %d ugnot (1 GNOT) klash pays in one payment", label, x402MaxPrice))
		default:
			return offer, chain, nil
		}
	}
	if len(why) == 0 {
		return types.PaymentRequirements{}, "", errors.New("the seller's 402 lists no way to pay")
	}
	return types.PaymentRequirements{}, "", errors.New("klash cannot pay any of the seller's offers:\n- " + strings.Join(why, "\n- "))
}

// withinX402Max reports whether amount, a whole number of ugnot, is at most
// x402MaxPrice. Anything else is left for Prepare to refuse with its reason.
func withinX402Max(amount string) bool {
	n, err := strconv.ParseUint(amount, 10, 64)
	return err != nil || n <= x402MaxPrice
}

// gnokeyEntry is a key as gnokey lists it.
type gnokeyEntry struct {
	name string
	pub  crypto.PubKey
}

// gnokeyListLine is one key in `gnokey list`'s output:
// "0. name (local) - addr: g1... pub: gpub1..., path: <nil>".
var gnokeyListLine = regexp.MustCompile(`^\d+\. (.+) \((\w+)\) - addr: (\S+) pub: ([^,\s]+),`)

// gnokeyKey finds the key named nameOrAddress in gnokey's keybase at home,
// with its public key, by asking gnokey: the keybase stays gnokey's, and
// listing it needs no password.
func gnokeyKey(ctx context.Context, home, nameOrAddress string) (gnokeyEntry, error) {
	args := []string{"list"}
	if home != "" {
		args = append(args, "-home", home)
	}
	out, code, err := runGnokey(ctx, "", args, &bytes.Buffer{})
	if err != nil || code != 0 {
		return gnokeyEntry{}, fmt.Errorf("gnokey list failed: %s", strings.TrimSpace(cmp.Or(out, fmt.Sprint(err))))
	}
	for line := range strings.SplitSeq(out, "\n") {
		m := gnokeyListLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || (m[1] != nameOrAddress && m[3] != nameOrAddress) {
			continue
		}
		name, kind, addr, bech := m[1], m[2], m[3], m[4]
		if kind == "offline" || kind == "multi" {
			return gnokeyEntry{}, fmt.Errorf("key %s is an %s key, which cannot sign here", name, kind)
		}
		pub, err := crypto.PubKeyFromBech32(bech)
		if err != nil || pub.Address().String() != addr {
			return gnokeyEntry{}, fmt.Errorf("gnokey lists key %s with a public key that is not its address's", name)
		}
		return gnokeyEntry{name: name, pub: pub}, nil
	}
	if home == "" {
		// Models drop optional parameters, home among them, even when the user
		// named it; say which keybase this was, so the next call can add it.
		return gnokeyEntry{}, fmt.Errorf("gnokey's default keybase holds no key %q; if the user named a gnokey home that holds it, call again with that home; otherwise the user adds the key with gnokey add, in a terminal", nameOrAddress)
	}
	return gnokeyEntry{}, fmt.Errorf("gnokey's keybase at %s holds no key %q; the user adds keys with gnokey add, in a terminal", home, nameOrAddress)
}

// askX402Password asks the user for the key's password in a masked prompt
// that shows exactly what the signature pays.
func askX402Password(ctx context.Context, questions question.Service, sessionID, callID string, params X402FetchParams, key gnokeyEntry, payment *exact.Payment) (string, error) {
	send := payment.Tx.Msgs[0].(bank.MsgSend)
	from := fmt.Sprintf("from %s (key %s)", send.FromAddress, key.name)
	if !payment.SessionAddr.IsZero() {
		from = fmt.Sprintf("from %s, signed by its session key %s", send.FromAddress, key.name)
	}
	head := fmt.Sprintf("**Pays %s** to `%s` on **%s**, plus a network fee of %s, %s, for:\n\n",
		gnotAmount(send.Amount[0]), send.ToAddress, payment.ChainID, gnotAmount(payment.Tx.Fee.GasFee), from)
	tail := "\n\ngnokey signs one bank send of exactly this, and the seller's facilitator settles it. " +
		"The password goes to gnokey's standard input and nowhere else; the model never sees it. Esc declines."
	if payment.Tx.Memo != "" {
		tail = fmt.Sprintf("\n\nMemo `%s`.", payment.Tx.Memo) + tail
	}
	// The permission dialog showed the whole URL; here a very long one loses
	// its tail to the description limit.
	resource := params.URL
	if room := question.MaxDescriptionLength - len(head) - len(tail) - len("``"); len(resource) > room {
		resource = resource[:max(room-3, 0)] + "..."
	}
	answers, err := questions.Ask(ctx, question.Request{
		SessionID:  sessionID,
		ToolCallID: callID,
		Questions: []question.Question{{
			Type:        question.TypeFreeText,
			Secret:      true,
			Label:       "Password",
			Text:        "Password for key " + key.name + " to pay",
			Description: head + "`" + resource + "`" + tail,
		}},
	})
	if err != nil {
		return "", err
	}
	if len(answers) == 0 || answers[0].FillInText == "" {
		return "", errors.New("no password was given")
	}
	return answers[0].FillInText, nil
}

// gnotAmount shows coin in GNOT beside its exact amount, "0.25 GNOT
// (250000ugnot)"; a coin in another denomination is shown as it is.
func gnotAmount(coin std.Coin) string {
	if coin.Denom != x402Asset {
		return coin.String()
	}
	whole, frac := coin.Amount/1_000_000, coin.Amount%1_000_000
	gnot := strconv.FormatInt(whole, 10)
	if frac != 0 {
		gnot += "." + strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
	}
	return fmt.Sprintf("%s GNOT (%s)", gnot, coin)
}

// gnokeySignPayment has gnokey sign payment with the key named name, and
// returns the signature. The unsigned transaction goes to gnokey in a file of
// its own, the password on gnokey's standard input; gnokey writes the
// signature alone to a second file, and the directory holding both is removed
// afterwards. Neither file holds a secret.
func gnokeySignPayment(ctx context.Context, home, name, password string, payment *exact.Payment) ([]byte, error) {
	dir, err := os.MkdirTemp("", "klash-x402-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	txPath := filepath.Join(dir, "payment.json")
	sigPath := filepath.Join(dir, "signature.json")
	unsigned, err := amino.MarshalJSON(payment.Tx)
	if err != nil {
		return nil, fmt.Errorf("encode the payment for gnokey: %w", err)
	}
	if err := os.WriteFile(txPath, unsigned, 0o600); err != nil {
		return nil, err
	}

	args := []string{"sign", passwordStdinFlag, "-quiet"}
	if home != "" {
		args = append(args, "-home", home)
	}
	args = append(args,
		"-tx-path", txPath,
		"-chainid", payment.ChainID,
		"-account-number", strconv.FormatUint(payment.AccountNumber, 10),
		"-account-sequence", strconv.FormatUint(payment.Sequence, 10),
		"-output-document", sigPath,
		name,
	)
	signCtx, cancel := context.WithTimeout(ctx, x402SignTimeout)
	defer cancel()
	out, code, err := runGnokey(signCtx, dir, args, bytes.NewBufferString(password+"\n"))
	// gnokey never echoes the password; this makes sure of it.
	out = strings.TrimSpace(strings.ReplaceAll(out, password, "[password]"))
	if err != nil {
		return nil, fmt.Errorf("gnokey did not finish signing: %v %s", err, out)
	}
	if code != 0 {
		return nil, fmt.Errorf("gnokey could not sign the payment (exit %d): %s", code, out)
	}
	raw, err := os.ReadFile(sigPath)
	if err != nil {
		return nil, fmt.Errorf("gnokey wrote no signature: %w", err)
	}
	var sig std.Signature
	if err := amino.UnmarshalJSON(raw, &sig); err != nil {
		return nil, fmt.Errorf("read gnokey's signature: %w", err)
	}
	return sig.Signature, nil
}

// x402Codec reads the x402 headers of a seller's answer; upstream's HTTP
// client is one.
type x402Codec interface {
	GetPaymentSettleResponse(headers map[string]string) (*x402.SettleResponse, error)
	GetPaymentRequiredResponse(headers map[string]string, body []byte) (x402.PaymentRequired, error)
}

// x402Outcome reports what the paid request came back with: the resource and
// the settlement that paid for it, or why nothing was paid.
func x402Outcome(codec x402Codec, resp *http.Response, body []byte, format string, payment *exact.Payment) fantasy.ToolResponse {
	headers := headerMap(resp.Header)
	settled, _ := codec.GetPaymentSettleResponse(headers)
	send := payment.Tx.Msgs[0].(bank.MsgSend)

	if resp.StatusCode == http.StatusOK {
		content, err := formatFetched(body, resp.Header.Get("Content-Type"), format)
		if err != nil {
			content = "(the content could not be shown: " + err.Error() + ")"
		}
		receipt := fmt.Sprintf("Paid %s to %s on %s, plus a %s network fee.", gnotAmount(send.Amount[0]), send.ToAddress, payment.ChainID, gnotAmount(payment.Tx.Fee.GasFee))
		if settled != nil && settled.Transaction != "" {
			receipt += fmt.Sprintf(" Transaction %s, payer %s.", settled.Transaction, settled.Payer)
		} else {
			receipt += " The seller sent no settlement receipt."
		}
		return fantasy.NewTextResponse(receipt + "\n\n" + content)
	}

	reason := ""
	if settled != nil {
		reason = strings.TrimSpace(settled.ErrorReason)
	}
	if reason == "" && resp.StatusCode == http.StatusPaymentRequired {
		if challenge, err := codec.GetPaymentRequiredResponse(headers, body); err == nil {
			reason = strings.TrimSpace(challenge.Error)
		}
	}
	switch {
	case reason == "settlement_pending" && settled != nil:
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Not confirmed yet: the chain admitted transaction %s but no block has executed it, so the seller withheld the resource. The payment may still go through; look the hash up before paying again.", settled.Transaction))
	case resp.StatusCode == http.StatusPaymentRequired:
		msg := "Not paid: the seller refused the payment"
		if reason != "" {
			msg += " (" + reason + ")"
		}
		if settled != nil && settled.Transaction != "" {
			msg += "; transaction " + settled.Transaction
		}
		return fantasy.NewTextErrorResponse(msg + ".")
	default:
		return fantasy.NewTextErrorResponse(fmt.Sprintf("The seller answered %d %s to the paid request. A seller settles only after it serves, so nothing should have been taken, but the signed payment stays valid until the paying account sends another transaction.", resp.StatusCode, http.StatusText(resp.StatusCode)))
	}
}
