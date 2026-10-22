package x402gno

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gnolang/gno/tm2/pkg/amino"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/std"
)

// rpcTimeout bounds one call to a node. Every read comes before anything is
// signed, so a read abandoned costs nothing.
const rpcTimeout = 15 * time.Second

// maxRPCResponse caps what one answer from a node may weigh. The answers read
// here are a status, a gas price and one account, each a few kilobytes.
const maxRPCResponse = 1 << 20

// RPCNode reads a gno chain through a node's JSON-RPC endpoint over plain
// HTTP: its status, and the auth module's queries for an account, a session
// and the gas price.
//
// It is what gnoclient's queries do, with gno's own decoders for what comes
// back, minus the client: gnoclient brings the Gno VM with it, and a buyer has
// no use for one. It holds no state beyond the endpoint, so one value is safe
// to share across concurrent payments.
type RPCNode struct {
	endpoint string
	client   *http.Client
}

var _ Node = (*RPCNode)(nil)

// NewRPCNode returns the node at remote: a URL such as
// https://rpc.onyx.testnets.gno.land:443, a tcp:// address as gnodev prints
// it, or a bare host:port as gnokey's -remote takes it, read over http.
func NewRPCNode(remote string) (*RPCNode, error) {
	endpoint, err := rpcEndpoint(remote)
	if err != nil {
		return nil, err
	}
	return &RPCNode{endpoint: endpoint, client: &http.Client{Timeout: rpcTimeout}}, nil
}

// rpcEndpoint is remote as an http(s) URL with no trailing slash.
func rpcEndpoint(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	switch {
	case remote == "":
		return "", errors.New("no node to read the chain from")
	case strings.HasPrefix(remote, "tcp://"):
		remote = "http://" + strings.TrimPrefix(remote, "tcp://")
	case !strings.Contains(remote, "://"):
		remote = "http://" + remote
	}
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not a node address: give an http(s) URL or host:port", remote)
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// Endpoint is the URL the node is read at.
func (n *RPCNode) Endpoint() string { return n.endpoint }

// NodeChainID is the chain id the node reports for itself.
func (n *RPCNode) NodeChainID(ctx context.Context) (string, error) {
	var status struct {
		NodeInfo struct {
			Network string `json:"network"`
		} `json:"node_info"`
	}
	if err := n.call(ctx, "status", nil, &status); err != nil {
		return "", err
	}
	if status.NodeInfo.Network == "" {
		return "", errors.New("the node's status names no chain")
	}
	return status.NodeInfo.Network, nil
}

// Account reads addr's account. An address the chain holds no account for is
// std.ErrUnknownAddress, as gnoclient reports it.
func (n *RPCNode) Account(ctx context.Context, addr crypto.Address) (std.Account, error) {
	data, err := n.query(ctx, "auth/accounts/"+addr.String())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, std.ErrUnknownAddress("unknown address: " + addr.String())
	}
	var wrapped struct {
		BaseAccount json.RawMessage `json:"BaseAccount"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("decode the account of %s: %w", addr, err)
	}
	return decodeBaseAccount(wrapped.BaseAccount)
}

// SessionAccount reads the session account session holds under master. A
// session the chain does not hold, such as a revoked one, is an error.
func (n *RPCNode) SessionAccount(ctx context.Context, master, session crypto.Address) (std.Account, error) {
	data, err := n.query(ctx, "auth/accounts/"+master.String()+"/session/"+session.String())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, std.ErrUnknownAddress("no session " + session.String() + " under " + master.String())
	}
	var wrapped struct {
		BaseSessionAccount struct {
			BaseAccount json.RawMessage `json:"BaseAccount"`
		} `json:"BaseSessionAccount"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("decode session %s of %s: %w", session, master, err)
	}
	return decodeBaseAccount(wrapped.BaseSessionAccount.BaseAccount)
}

// decodeBaseAccount decodes the account object gno.land's accounts and
// sessions both wrap, with amino, as gnoclient does.
func decodeBaseAccount(raw json.RawMessage) (std.Account, error) {
	if len(raw) == 0 {
		return nil, errors.New("the node's answer holds no account")
	}
	var acc std.BaseAccount
	if err := amino.UnmarshalJSON(raw, &acc); err != nil {
		return nil, fmt.Errorf("decode the account: %w", err)
	}
	return &acc, nil
}

// GasPrice reads the gas price of the chain's last block, as gnokey reads it
// to estimate a fee. A node answering with nothing reports no price.
func (n *RPCNode) GasPrice(ctx context.Context) (*std.GasPrice, error) {
	data, err := n.query(ctx, "auth/gasprice")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var price std.GasPrice
	if err := amino.UnmarshalJSON(data, &price); err != nil {
		return nil, fmt.Errorf("decode gas price %q: %w", data, err)
	}
	return &price, nil
}

// query runs the ABCI query path and returns the data it answers with. A
// query the chain refuses is an error carrying the chain's log.
func (n *RPCNode) query(ctx context.Context, path string) ([]byte, error) {
	var res struct {
		Response struct {
			ResponseBase struct {
				Error json.RawMessage `json:"Error"`
				Data  []byte          `json:"Data"`
				Log   string          `json:"Log"`
			} `json:"ResponseBase"`
		} `json:"response"`
	}
	params := url.Values{"path": {strconv.Quote(path)}}
	if err := n.call(ctx, "abci_query", params, &res); err != nil {
		return nil, err
	}
	base := res.Response.ResponseBase
	if len(base.Error) > 0 && string(base.Error) != "null" {
		return nil, fmt.Errorf("query %s: the chain refused it: %s", path, cmp.Or(base.Log, string(base.Error)))
	}
	return base.Data, nil
}

// call makes one JSON-RPC call in its URI form, GET <endpoint>/<method>?<params>,
// and decodes the result into result.
func (n *RPCNode) call(ctx context.Context, method string, params url.Values, result any) error {
	target := n.endpoint + "/" + method
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	res, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s at %s: %w", method, n.endpoint, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxRPCResponse))
	if err != nil {
		return fmt.Errorf("%s at %s: %w", method, n.endpoint, err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%s at %s: status %d, not a JSON-RPC answer", method, n.endpoint, res.StatusCode)
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s at %s: %s", method, n.endpoint, strings.TrimSpace(envelope.Error.Message+" "+envelope.Error.Data))
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("%s at %s: %w", method, n.endpoint, err)
	}
	return nil
}
