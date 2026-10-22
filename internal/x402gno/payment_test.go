package x402gno_test

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	// test1Mnemonic is gno's well-known test1 key, the payer. Nothing here is
	// broadcast.
	test1Mnemonic = "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast"
	test1Address  = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5"

	// sessionMnemonic is a session key, public like test1's and never funded.
	sessionMnemonic = "dance carry grunt uphold cloth wire usage usage clock reduce surprise page destroy reopen multiply away defense promote person web cigar immense armed enough"

	chainID = "dev"
	payTo   = "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq"

	// The account state the fake node reports, the same numbers the
	// TypeScript mechanism signed its fixtures over: test1's own account, and
	// the session account under it.
	accountNumber   = 12
	sequence        = 7
	sessionNumber   = 34
	sessionSequence = 3
)

// offer is 250000ugnot to payTo on gno:dev, with the memo the TypeScript
// fixtures bind.
func offer() types.PaymentRequirements {
	return types.PaymentRequirements{
		Scheme:            "exact",
		Network:           "gno:" + chainID,
		Asset:             "ugnot",
		Amount:            "250000",
		PayTo:             payTo,
		MaxTimeoutSeconds: 300,
		Extra:             map[string]any{"areFeesSponsored": false, "memo": "x402-interop"},
	}
}

// key is a key held in memory, the way gnokey holds it, that signs what it is
// given.
type key struct {
	kb  keys.Keybase
	pub crypto.PubKey
}

func keyFrom(t *testing.T, mnemonic string) key {
	t.Helper()
	kb := keys.NewInMemory()
	info, err := kb.CreateAccount("k", mnemonic, "", "", 0, 0)
	require.NoError(t, err)
	return key{kb: kb, pub: info.GetPubKey()}
}

func (k key) sign(t *testing.T, msg []byte) []byte {
	t.Helper()
	signature, _, err := k.kb.Sign("k", "", msg)
	require.NoError(t, err)
	return signature
}

// fakeNode is a scripted chain. reads counts the reads that reached it.
type fakeNode struct {
	chainID  string
	account  std.Account
	session  std.Account
	gasPrice *std.GasPrice

	accountErr error
	reads      int
}

var _ x402gno.Node = (*fakeNode)(nil)

func newFakeNode() *fakeNode {
	return &fakeNode{
		chainID:  chainID,
		account:  &std.BaseAccount{AccountNumber: accountNumber, Sequence: sequence},
		session:  &std.BaseAccount{AccountNumber: sessionNumber, Sequence: sessionSequence},
		gasPrice: &std.GasPrice{Gas: 1000, Price: std.Coin{Denom: "ugnot", Amount: 1}},
	}
}

func (n *fakeNode) NodeChainID(context.Context) (string, error) { n.reads++; return n.chainID, nil }

func (n *fakeNode) Account(context.Context, crypto.Address) (std.Account, error) {
	n.reads++
	return n.account, n.accountErr
}

func (n *fakeNode) SessionAccount(context.Context, crypto.Address, crypto.Address) (std.Account, error) {
	n.reads++
	return n.session, nil
}

func (n *fakeNode) GasPrice(context.Context) (*std.GasPrice, error) {
	n.reads++
	return n.gasPrice, nil
}

// pay prepares the payment for requirements, signs it with k as gnokey would
// sign it, and returns the transaction the payload carries.
func pay(t *testing.T, k key, master crypto.Address, requirements types.PaymentRequirements) []byte {
	t.Helper()
	payment, err := x402gno.Prepare(context.Background(), newFakeNode(), k.pub, master, requirements, "")
	require.NoError(t, err)
	signBytes, err := payment.SignBytes()
	require.NoError(t, err)
	payload, err := payment.Payload(k.sign(t, signBytes))
	require.NoError(t, err)
	transaction, ok := payload.Payload["transaction"].(string)
	require.True(t, ok)
	raw, err := base64.StdEncoding.DecodeString(transaction)
	require.NoError(t, err)
	return raw
}

// fixture is a payment the TypeScript mechanism in gnoverse/x402 signed for
// offer against the fake node's account state, as its base64 decodes.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	require.NoError(t, err)
	return raw
}

func decode(t *testing.T, raw []byte) std.Tx {
	t.Helper()
	var tx std.Tx
	require.NoError(t, amino.Unmarshal(raw, &tx))
	return tx
}

// TestAPaymentIsTheTypeScriptMechanismsByteForByte holds this copy to the
// payment the reference client signs for the same offer, key and chain state:
// the same transaction under the same signature. amino writes a zero
// session_addr that the TypeScript encoding leaves out, so the bytes are
// compared through amino.
func TestAPaymentIsTheTypeScriptMechanismsByteForByte(t *testing.T) {
	theirs := fixture(t, "tsclient_master_payment.b64")

	ours := pay(t, keyFrom(t, test1Mnemonic), crypto.Address{}, offer())

	require.Equal(t, decode(t, theirs).Signatures[0].Signature, decode(t, ours).Signatures[0].Signature)
	require.Equal(t, amino.MustMarshal(decode(t, theirs)), ours)
}

// TestASessionPaymentIsTheTypeScriptMechanismsByteForByte: as a session key,
// the bytes are identical as sent.
func TestASessionPaymentIsTheTypeScriptMechanismsByteForByte(t *testing.T) {
	theirs := fixture(t, "tsclient_session_payment.b64")

	ours := pay(t, keyFrom(t, sessionMnemonic), crypto.MustAddressFromString(test1Address), offer())

	require.Equal(t, theirs, ours)
}

// TestAPreparedPaymentShowsWhatItPays: what Prepare hands back is what a
// person approves before anything is signed — the price, the payee, the fee
// and the memo — and what the signature must commit to.
func TestAPreparedPaymentShowsWhatItPays(t *testing.T) {
	k := keyFrom(t, test1Mnemonic)

	payment, err := x402gno.Prepare(context.Background(), newFakeNode(), k.pub, crypto.Address{}, offer(), "")
	require.NoError(t, err)

	send := payment.Tx.Msgs[0].(bank.MsgSend)
	require.Equal(t, test1Address, send.FromAddress.String())
	require.Equal(t, payTo, send.ToAddress.String())
	require.Equal(t, "250000ugnot", send.Amount.String())
	require.Equal(t, "20000ugnot", payment.Tx.Fee.GasFee.String())
	require.Equal(t, int64(10_000_000), payment.Tx.Fee.GasWanted)
	require.Equal(t, "x402-interop", payment.Tx.Memo)
	require.Empty(t, payment.Tx.Signatures)
	require.Equal(t, chainID, payment.ChainID)
	require.Equal(t, uint64(accountNumber), payment.AccountNumber)
	require.Equal(t, uint64(sequence), payment.Sequence)
	require.True(t, payment.SessionAddr.IsZero())
}

// TestPayloadRefusesASignatureOverAnythingElse: a signature that is not the
// payment key's over this payment's sign doc never becomes a payload.
func TestPayloadRefusesASignatureOverAnythingElse(t *testing.T) {
	k := keyFrom(t, test1Mnemonic)
	payment, err := x402gno.Prepare(context.Background(), newFakeNode(), k.pub, crypto.Address{}, offer(), "")
	require.NoError(t, err)
	signBytes, err := payment.SignBytes()
	require.NoError(t, err)

	otherChain, err := payment.Tx.GetSignBytes("test12", payment.AccountNumber, payment.Sequence)
	require.NoError(t, err)
	cases := map[string][]byte{
		"another key":   keyFrom(t, sessionMnemonic).sign(t, signBytes),
		"another chain": k.sign(t, otherChain),
		"nothing":       nil,
	}
	for name, signature := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := payment.Payload(signature)
			require.ErrorContains(t, err, "the signature does not verify")
		})
	}
}

// TestPayloadTakesTheLegacyRendering: gnokey builds before gnolang/gno#6173
// render the fee as gas_wanted/gas_fee, which the chain still accepts, so a
// payment they sign is accepted too.
func TestPayloadTakesTheLegacyRendering(t *testing.T) {
	k := keyFrom(t, test1Mnemonic)
	payment, err := x402gno.Prepare(context.Background(), newFakeNode(), k.pub, crypto.Address{}, offer(), "")
	require.NoError(t, err)
	legacy, err := payment.Tx.GetSignBytesLegacy(payment.ChainID, payment.AccountNumber, payment.Sequence)
	require.NoError(t, err)

	_, err = payment.Payload(k.sign(t, legacy))
	require.NoError(t, err)
}

// TestRequirementsThatCannotBePaidAreRefusedUnread: the offer comes from a
// seller, so a malformed or foreign one is refused as requirements before the
// chain is read.
func TestRequirementsThatCannotBePaidAreRefusedUnread(t *testing.T) {
	cases := map[string]func(*types.PaymentRequirements){
		"another namespace":      func(r *types.PaymentRequirements) { r.Network = "eip155:8453" },
		"a wildcard network":     func(r *types.PaymentRequirements) { r.Network = "gno:*" },
		"another scheme":         func(r *types.PaymentRequirements) { r.Scheme = "upto" },
		"an asset in upper case": func(r *types.PaymentRequirements) { r.Asset = "UGNOT" },
		"an amount of zero":      func(r *types.PaymentRequirements) { r.Amount = "0" },
		"a dollar amount":        func(r *types.PaymentRequirements) { r.Amount = "$0.25" },
		"an amount no coin holds": func(r *types.PaymentRequirements) {
			r.Amount = "9223372036854775808"
		},
		"no payTo":                 func(r *types.PaymentRequirements) { r.PayTo = "" },
		"a payTo in upper case":    func(r *types.PaymentRequirements) { r.PayTo = strings.ToUpper(payTo) },
		"a memo that is no string": func(r *types.PaymentRequirements) { r.Extra["memo"] = 42 },
		"a memo over the cap": func(r *types.PaymentRequirements) {
			r.Extra["memo"] = strings.Repeat("m", 257)
		},
		"another transfer method": func(r *types.PaymentRequirements) { r.Extra["assetTransferMethod"] = "eip3009" },
		"another flow":            func(r *types.PaymentRequirements) { r.Extra["paymentFlow"] = "upfront" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			requirements := offer()
			mutate(&requirements)
			node := newFakeNode()

			_, err := x402gno.Prepare(context.Background(), node, keyFrom(t, test1Mnemonic).pub, crypto.Address{}, requirements, "")

			require.ErrorIs(t, err, x402gno.ErrInvalidRequirements)
			require.Zero(t, node.reads)
		})
	}
}

// TestAPriceOverTheCapIsRefusedUnread: the buyer's cap bounds the price, and
// a price at it is paid.
func TestAPriceOverTheCapIsRefusedUnread(t *testing.T) {
	pub := keyFrom(t, test1Mnemonic).pub
	node := newFakeNode()

	_, err := x402gno.Prepare(context.Background(), node, pub, crypto.Address{}, offer(), "249999")
	require.ErrorIs(t, err, x402gno.ErrExceedsSpendCap)
	require.Zero(t, node.reads)

	_, err = x402gno.Prepare(context.Background(), newFakeNode(), pub, crypto.Address{}, offer(), "250000")
	require.NoError(t, err)
}

// TestAnOfferForAChainTheNodeDoesNotServeIsRefused: the payment would commit
// to the offer's chain over another chain's account state.
func TestAnOfferForAChainTheNodeDoesNotServeIsRefused(t *testing.T) {
	node := newFakeNode()
	node.chainID = "test12"

	_, err := x402gno.Prepare(context.Background(), node, keyFrom(t, test1Mnemonic).pub, crypto.Address{}, offer(), "")

	require.ErrorContains(t, err, "the offer is for chain dev and the node serves test12")
}

// TestAnAccountTheChainDoesNotHoldIsNamed tells the buyer what to fund.
func TestAnAccountTheChainDoesNotHoldIsNamed(t *testing.T) {
	node := newFakeNode()
	node.accountErr = std.ErrUnknownAddress("unknown address: " + test1Address)

	_, err := x402gno.Prepare(context.Background(), node, keyFrom(t, test1Mnemonic).pub, crypto.Address{}, offer(), "")

	require.ErrorContains(t, err, "the chain holds no account for "+test1Address+"; fund it")
	_, unknown := errors.AsType[std.UnknownAddressError](err)
	require.True(t, unknown)
}

// TestTheFeeFollowsTheChainsGasPrice: twice the price for the gas reserved,
// rounded up; a chain reporting none gets a fee clear of any node's minimum.
func TestTheFeeFollowsTheChainsGasPrice(t *testing.T) {
	cases := map[string]struct {
		price *std.GasPrice
		want  string
	}{
		"onyx-1's price":    {&std.GasPrice{Gas: 1000, Price: std.Coin{Denom: "ugnot", Amount: 1}}, "20000ugnot"},
		"rounded up":        {&std.GasPrice{Gas: 7, Price: std.Coin{Denom: "ugnot", Amount: 3}}, "8571429ugnot"},
		"no price reported": {nil, "1000000ugnot"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			node := newFakeNode()
			node.gasPrice = tc.price

			payment, err := x402gno.Prepare(context.Background(), node, keyFrom(t, test1Mnemonic).pub, crypto.Address{}, offer(), "")

			require.NoError(t, err)
			require.Equal(t, tc.want, payment.Tx.Fee.GasFee.String())
		})
	}
}
