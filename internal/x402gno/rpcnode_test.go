package x402gno_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/charmbracelet/crush/internal/x402gno"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/require"
)

// The chain's answers, as gno.land writes them: amino JSON, indented. The gas
// price is what onyx-1 answered on 2026-10-22.
const (
	onyxGasPrice = "{\n  \"gas\": \"1000\",\n  \"price\": \"1ugnot\"\n}"

	accountJSON = `{
  "BaseAccount": {
    "address": "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5",
    "coins": "1000ugnot",
    "public_key": null,
    "account_number": "12",
    "sequence": "7"
  },
  "attributes": "0"
}`

	sessionJSON = `{
  "BaseSessionAccount": {
    "BaseAccount": {
      "address": "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq",
      "coins": "",
      "public_key": null,
      "account_number": "34",
      "sequence": "3"
    },
    "master_address": "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5",
    "expires_at": "0"
  }
}`
)

// stubChain is a node's JSON-RPC endpoint answering status with network, and
// each ABCI query path in answers with its data, or with nothing when the path
// is absent. A path in refusals is answered with the chain's error instead.
func stubChain(t *testing.T, network string, answers map[string]string, refusals map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"","result":{"node_info":{"network":%q}}}`, network)
		case "/abci_query":
			path, err := strconv.Unquote(r.URL.Query().Get("path"))
			require.NoError(t, err, "the path parameter is a JSON string")
			if log, refused := refusals[path]; refused {
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"","result":{"response":{"ResponseBase":{"Error":{"@type":"/std.SessionNotFoundError"},"Data":null,"Log":%q}}}}`, log)
				return
			}
			data := "null"
			if answer, ok := answers[path]; ok {
				data = strconv.Quote(base64.StdEncoding.EncodeToString([]byte(answer)))
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"","result":{"response":{"ResponseBase":{"Error":null,"Data":%s,"Log":""}}}}`, data)
		default:
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"","error":{"code":-32601,"message":"Method not found"}}`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestTheNodeIsReadAsGnoLandWritesIt decodes each answer a payment needs from
// a node in the form a gno.land node gives it.
func TestTheNodeIsReadAsGnoLandWritesIt(t *testing.T) {
	master := crypto.MustAddressFromString(test1Address)
	session := crypto.MustAddressFromString(payTo)
	server := stubChain(t, "onyx-1", map[string]string{
		"auth/gasprice":                                       onyxGasPrice,
		"auth/accounts/" + test1Address:                       accountJSON,
		"auth/accounts/" + test1Address + "/session/" + payTo: sessionJSON,
	}, nil)
	node, err := x402gno.NewRPCNode(server.URL)
	require.NoError(t, err)
	ctx := context.Background()

	served, err := node.NodeChainID(ctx)
	require.NoError(t, err)
	require.Equal(t, "onyx-1", served)

	price, err := node.GasPrice(ctx)
	require.NoError(t, err)
	require.Equal(t, &std.GasPrice{Gas: 1000, Price: std.Coin{Denom: "ugnot", Amount: 1}}, price)

	acc, err := node.Account(ctx, master)
	require.NoError(t, err)
	require.Equal(t, uint64(12), acc.GetAccountNumber())
	require.Equal(t, uint64(7), acc.GetSequence())
	require.Equal(t, master, acc.GetAddress())

	sess, err := node.SessionAccount(ctx, master, session)
	require.NoError(t, err)
	require.Equal(t, uint64(34), sess.GetAccountNumber())
	require.Equal(t, uint64(3), sess.GetSequence())
}

// TestAnAddressTheChainHoldsNoAccountForIsUnknown: the node answers null, and
// the payment reports the address to fund rather than a decode error.
func TestAnAddressTheChainHoldsNoAccountForIsUnknown(t *testing.T) {
	node, err := x402gno.NewRPCNode(stubChain(t, "dev", nil, nil).URL)
	require.NoError(t, err)

	_, err = node.Account(context.Background(), crypto.MustAddressFromString(test1Address))

	_, unknown := errors.AsType[std.UnknownAddressError](err)
	require.True(t, unknown, "got %v", err)
}

// TestARevokedSessionIsAnError: the chain refuses the query with its own
// error, which is reported with the chain's log.
func TestARevokedSessionIsAnError(t *testing.T) {
	path := "auth/accounts/" + test1Address + "/session/" + payTo
	node, err := x402gno.NewRPCNode(stubChain(t, "dev", nil, map[string]string{path: "session not found"}).URL)
	require.NoError(t, err)

	_, err = node.SessionAccount(context.Background(), crypto.MustAddressFromString(test1Address), crypto.MustAddressFromString(payTo))

	require.ErrorContains(t, err, "session not found")
}

// TestAChainReportingNoGasPriceIsUnpriced: no data is no price, which the
// payment answers with its unpriced fee, not an error.
func TestAChainReportingNoGasPriceIsUnpriced(t *testing.T) {
	node, err := x402gno.NewRPCNode(stubChain(t, "dev", nil, nil).URL)
	require.NoError(t, err)

	price, err := node.GasPrice(context.Background())

	require.NoError(t, err)
	require.Nil(t, price)
}

// TestANodeThatIsNotThereIsAnError: nothing listening, and a server that is
// no JSON-RPC endpoint, both fail the read rather than answer for the chain.
func TestANodeThatIsNotThereIsAnError(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	notRPC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
	}))
	t.Cleanup(notRPC.Close)

	for _, url := range []string{closed.URL, notRPC.URL} {
		node, err := x402gno.NewRPCNode(url)
		require.NoError(t, err)
		_, err = node.NodeChainID(context.Background())
		require.Error(t, err)
	}
}

// TestANodeAddressIsReadTheWaysGnoToolsWriteIt: a URL, gnodev's tcp://
// address, and gnokey's bare -remote all name an http endpoint.
func TestANodeAddressIsReadTheWaysGnoToolsWriteIt(t *testing.T) {
	cases := map[string]string{
		"https://rpc.onyx.testnets.gno.land:443": "https://rpc.onyx.testnets.gno.land:443",
		"https://rpc.gno.land:443/":              "https://rpc.gno.land:443",
		"tcp://127.0.0.1:26657":                  "http://127.0.0.1:26657",
		"127.0.0.1:26657":                        "http://127.0.0.1:26657",
	}
	for remote, want := range cases {
		node, err := x402gno.NewRPCNode(remote)
		require.NoError(t, err, remote)
		require.Equal(t, want, node.Endpoint())
	}
	for _, bad := range []string{"", "ftp://example.com", "http://"} {
		_, err := x402gno.NewRPCNode(bad)
		require.Error(t, err, bad)
	}
}
