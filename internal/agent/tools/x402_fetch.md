Fetch a URL that charges for access through x402, paying for it on a gno.land test chain with a gnokey key the user names. The user approves every payment: they see the price, the payee, the chain and the network fee, and type the key's password into a masked prompt; it goes straight to gnokey and you never see it. Use this only when the user wants to pay for the resource.

<usage>
- Use it when a URL answers 402 Payment Required (fetch reports that) and the user has said to pay; never on your own initiative
- Pass the key the user named: a gnokey key name or address; and whenever the user names a gnokey home, pass it as home on every call, or gnokey's default keybase is searched instead
- With a session key, pass master: the address of the account that registered the session, which pays within the limits the chain holds for it
- remote is a node on the chain the seller names; the public nodes of dev (127.0.0.1:26657), staging and onyx-1 are known, any other chain needs it
- It pays only on local and test chains (dev, staging, onyx-*, test*), only in ugnot, at most 1 GNOT per payment, and a network fee of at most 1 GNOT; mainnet is refused, because x402 on gno.land is unaudited, testnet-only work
- The fee follows the gas price the node reports; if a node reports a price that would make the fee more than 1 GNOT, the payment is refused: tell the user, and do not go looking for another node
- One call pays at most once, for exactly the price the seller names, plus the network fee
- Never ask the user for a password in chat; the prompt is the only place a password goes
- If the user declines (Esc), stop and ask them what to do; do not retry on your own
- An account the chain has never funded cannot pay: tell the user which address to fund (https://faucet.gno.land funds the test networks)
- In a non-interactive run this tool is absent: tell the user the URL charges, and how much
</usage>
