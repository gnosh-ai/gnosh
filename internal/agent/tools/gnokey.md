Run a gnokey command for the user: sign and broadcast a gno.land transaction (maketx addpkg, call, send or run; sign), broadcast a signed one, or query a chain. When the command needs the key's password, the user types it into a masked prompt in their terminal; it goes straight to gnokey and you never see it.

<usage>
- Pass the command line as you would type it, with or without the leading "gnokey", the key name last: `maketx addpkg -pkgpath gno.land/r/dev/counter -pkgdir . -gas-fee 1000000ugnot -gas-wanted 20000000 -chainid dev -remote 127.0.0.1:26657 devtest`
- Load the gno skill first: it has the networks, the gas numbers and the checks to run before a deploy
- Never add -insecure-password-stdin, and never ask the user for a password in chat; the prompt is the only place a password goes
- Read-only queries (`query vm/qrender -data "gno.land/r/dev/counter:" -remote 127.0.0.1:26657`) need no password and may also run through bash
- Key management (add, delete, rotate, import, export, generate) is refused: tell the user to run it in a terminal
- -chainid gnoland-1 or rpc.gno.land is mainnet and spends real GNOT: show the user the full command and get a clear yes before calling this tool
- If the user cancels the prompt (Esc), stop and ask them what to do; do not retry on your own
- In a non-interactive run this tool is absent: print the exact command for the user to run instead
</usage>
