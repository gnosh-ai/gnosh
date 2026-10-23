#!/usr/bin/env python3
"""Regenerates internal/x402gno/payment.go from the Go x402 client.

    scripts/sync-x402gno.py <checkout of github.com/jaekwon/x402>

payment.go is that repository's client/exact/scheme.go with three cuts: the
x402.SchemeNetworkClient that signs in process (klash signs through gnokey),
the facilitator import, whose three protocol constants are written out
instead, and the package documentation. Run it after the library changes,
then review the diff: anything beyond the library's own change means the
library moved under one of the cuts, and this script needs updating with it.
"""
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEST = os.path.join(HERE, "..", "internal", "x402gno", "payment.go")

HEADER = '''// Package x402gno is klash's copy of the client half of gno.land's x402
// "exact" payment mechanism: building a payment a seller's requirements ask
// for, checking it against the chain, and completing it with a signature made
// elsewhere. Klash has gnokey make that signature, so a key's password goes to
// gnokey and nowhere else.
//
// It is client/exact from github.com/jaekwon/x402, trimmed to what klash
// uses: Prepare and Payment, without the x402.SchemeNetworkClient that signs
// in process, and with the facilitator's three protocol constants written out
// here. Importing the package itself would link gnoclient, and with it the
// Gno VM, and the facilitator; this copy links gno's amino, std, crypto and
// bank packages only. scripts/sync-x402gno.py regenerates it from a checkout
// of the library.
//
// A payment is one bank send of exactly the price to the seller, signed and
// not broadcast; the seller's facilitator broadcasts it. The chain id it is
// signed for comes from the offer, and the node it reads must serve that
// chain.
package x402gno

'''

CONSTANTS = '''// The facilitator's protocol constants (github.com/jaekwon/x402,
// facilitator/exact), which a payment has to satisfy.
const (
	// assetTransferMethod is how a gno payment moves value: the payer signs a
	// bank send the facilitator broadcasts. An offer that omits
	// extra.assetTransferMethod means it.
	assetTransferMethod = "bank-send"

	// maxMemoBytes caps extra.memo. A payment signed against a longer memo is
	// one the facilitator never accepts.
	maxMemoBytes = 256
)

// chainIDReference is CAIP-2's grammar for the reference half of a network,
// the one a facilitator holds its own chain id to.
var chainIDReference = regexp.MustCompile(`^[-_a-zA-Z0-9]{1,32}$`)

'''


def cut(s, start, end):
    a, b = s.index(start), s.index(end)
    assert a < b, (start, end)
    return s[:a] + s[b:]


def replace_once(s, old, new):
    assert s.count(old) == 1, ("expected once", old[:70], s.count(old))
    return s.replace(old, new)


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    src = os.path.join(sys.argv[1], "client", "exact", "scheme.go")
    s = open(src).read()

    body = s[s.index("import ("):]
    body = cut(body, "// Signer holds the key a payment is signed with",
               "// Node is the chain access a payment needs before it is signed")
    body = cut(body, '// ExactGnoScheme signs gno payments for the "exact" scheme.',
               "// Payment is a payment built for a seller")
    body = replace_once(body, '\n\tfacilitatorexact "github.com/gnoverse/x402/facilitator/exact"\n', "\n")
    body = body.replace("facilitatorexact.AssetTransferMethod", "assetTransferMethod")
    body = body.replace("facilitatorexact.MaxMemoBytes", "maxMemoBytes")
    body = replace_once(body, "facilitatorexact.ValidChainID(chainID) != nil",
                        "!chainIDReference.MatchString(chainID)")
    body = replace_once(body, "// A gno buyer pays its own transaction fee",
                        CONSTANTS + "// A gno buyer pays its own transaction fee")
    body = replace_once(body,
                        "It is CreatePaymentPayload's first half, for a key held where this process\n"
                        "// cannot sign with it, such as by gnokey or on a hardware wallet, or for a\n"
                        "// buyer who wants a person to approve each price: show what Payment.Tx pays,\n"
                        "// have the key sign it, and hand the signature to Payload.",
                        "Show a person what Payment.Tx pays, have the key sign it, and hand the\n"
                        "// signature to Payload.")
    body = replace_once(body,
                        "// Payload attaches signature, PubKey's signature over the payment, and returns\n"
                        "// the scheme payload CreatePaymentPayload would: base64 of the signed std.Tx.",
                        "// Payload attaches signature, PubKey's signature over the payment, and returns\n"
                        "// the scheme payload: base64 of the signed std.Tx.")
    body = replace_once(body,
                        "the seller's facilitator broadcasts the payment. Implemented by the gnoclient\n"
                        "// adapter NewGnoclientNode and by fakes in tests.",
                        "the seller's facilitator broadcasts the payment. Implemented by RPCNode and\n"
                        "// by fakes in tests.")
    with open(DEST, "w") as f:
        f.write(HEADER + body)
    print("regenerated", os.path.relpath(DEST))


if __name__ == "__main__":
    main()
