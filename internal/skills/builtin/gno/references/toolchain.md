# The Gno toolchain

Every command below prints its flags with `-h`. Install:
`curl -fsSL https://raw.githubusercontent.com/gnolang/gno/master/misc/install.sh | sh`
or `make install` in a gno checkout. `gno env` prints `GNOROOT` (the gno
checkout the tools resolve stdlibs and examples from) and `GNOHOME` (keys
and the module cache).

## gno

| Command | What it does |
|---|---|
| `gno mod init <pkgpath>` | writes `gnomod.toml` |
| `gno mod tidy` | adds missing and drops unused requirements; converts an old `gno.mod` |
| `gno mod download` | fetches dependencies into `$GNOHOME/pkg/mod` (`gno clean -modcache` empties it) |
| `gno fmt -w .` | formats in place; `-diff` fails when a change is needed |
| `gno lint .` | static checks; `-v` verbose |
| `gno test [-v] [-run Name] [-timeout 2m] [-print-runtime-metrics] [-print-events] [-update-golden-tests] .` | runs tests and filetests |
| `gno run -expr 'println(F(1))' .` | evaluates an expression; `-debug` starts the debugger |
| `gno doc [-all] [-src] [-u] <pkg>[.Symbol]` | documentation of a stdlib or on-chain package |
| `gno tool transpile` | `.gno` to `.go`; `gno tool deplist` lists dependencies |
| `gno repl` | a REPL |

A `gnowork.toml` (empty file) at the root of a tree of packages lets
`gno test` and `gno lint` resolve them against each other.

## gnodev

`gnodev [local|staging] [flags] [package_dir...]`. Local is the default:
interactive keys, hot reload, unsafe `/reset` and `/reload` endpoints.

| Flag | Default | Purpose |
|---|---|---|
| `-node-rpc-listener` | `127.0.0.1:26657` | RPC; `gnokey -remote` must match |
| `-web-listener` | `127.0.0.1:8888` | gnoweb; `-no-web` disables it |
| `-chain-id` | `dev` | chain id to sign for |
| `-deploy-key` | devtest | deployer of the loaded packages |
| `-add-account <name|addr>[=<amount>]` | | premine another account (repeatable) |
| `-extra-root <dir>` | | more package trees to load (repeatable) |
| `-paths <pkgpath,...>` | | package paths to preload |
| `-no-examples` | | skip `$GNOROOT/examples` |
| `-no-watch`, `-no-replay` | | disable hot reload, or the replay of transactions across reloads |
| `-home` | `$GNOHOME` | keybase whose keys are premined |

Interactive keys while it runs: `H` help, `A` accounts and balances, `R`
reload, `Ctrl+R` reset, `Ctrl+S` save state, `E` export genesis.

## gnokey

Global flags on every subcommand: `-home <dir>` (keybase, default
`$GNOHOME`), `-remote <addr>` (default `127.0.0.1:26657`), `-quiet`,
`-insecure-password-stdin` (the gnokey tool adds this itself; never in a
bash command).

Keys (the user's own terminal; the tool refuses them):
`gnokey add <name>` (new mnemonic; `-recover` imports one; `-ledger`),
`gnokey list`, `gnokey delete <name>`, `gnokey rotate <name>`,
`gnokey export`/`import` (encrypted armor), `gnokey generate` (a mnemonic).

Transactions, `gnokey maketx <kind> [flags] <key>`:

| Flag | Purpose |
|---|---|
| `-chainid <id>` | chain to sign for |
| `-gas-fee <n>ugnot` | fee paid |
| `-gas-wanted <n>` | gas cap; simulation (`-simulate test`, the default) estimates use |
| `-broadcast=false` | print the unsigned transaction instead of signing |
| `-simulate only` | sign and simulate, do not broadcast; `skip` broadcasts without simulating |
| `-send <coins>` | coins attached, `100ugnot` |
| `-max-deposit <coins>` | cap on the storage deposit locked |
| `-memo <text>` | note on the transaction |

| Kind | Own flags |
|---|---|
| `addpkg` | `-pkgpath <path>` (must equal gnomod.toml's `module`), `-pkgdir <dir>` |
| `call` | `-pkgpath`, `-func <Name>`, `-args <v>` (one per argument, in order; none for a variadic with no values) |
| `send` | `-to <g1...>`, `-send <coins>` |
| `run` | the script path after the key; `package main`, `func main(cur realm)` |

Airgapped signing: `maketx ... -broadcast=false <key> > tx.json`, then on
the signing machine `gnokey sign -tx-path tx.json -chainid <id>
-account-number <n> -account-sequence <s> <key>` (both numbers from
`query auth/accounts/<addr>`), then `gnokey broadcast -remote <addr>
tx.json`. `gnokey verify` checks a signature; `gnokey multisign` combines
k-of-n signatures.

Queries, `gnokey query <path> [-data <bytes>] -remote <addr>`, free:

| Path | Returns |
|---|---|
| `auth/accounts/<addr>` | account number, sequence, gas-denom coins |
| `auth/gasprice` | current minimum gas price |
| `bank/balances/<addr>` | all balances |
| `bank/supply/<denom>` | total supply |
| `vm/qrender -data "<pkgpath>:<path>"` | `Render(path)` |
| `vm/qeval -data '<pkgpath>.<Expr>'` | a read-only expression, `pkg.Fn("x")` |
| `vm/qfuncs -data <pkgpath>` | exported functions with their signatures |
| `vm/qfile -data <pkgpath>[/<file>]` | the files, or one file |
| `vm/qdoc -data <pkgpath>` | documentation as JSON |
| `vm/qpaths -data <prefix>` | package paths under a prefix |
| `vm/qstorage -data <pkgpath>` | storage used and deposit locked |

JSON forms for programs: `vm/qeval_json`, `vm/qpkg_json`, `vm/qobject_json`,
`vm/qtype_json` (see `docs/builders/query-state-api.md`).

## Networks

| Network | chain id | RPC | Notes |
|---|---|---|---|
| gnodev | `dev` | `127.0.0.1:26657` | local; every keybase key premined |
| staging | `staging` | `https://rpc.staging.gno.land:443` | runs master; data may be lost on breaking changes; faucet |
| onyx (test17) | `onyx-1` | `https://rpc.onyx.testnets.gno.land:443` | mainnet's code, upgraded with it; faucet |
| mainnet | `gnoland-1` | `https://rpc.gno.land:443` | real GNOT, no faucet, launched 2026-09-12 |

Faucet for the test networks: https://faucet.gno.land (rate-limited per
address). gnoweb: https://staging.gno.land, https://onyx.testnets.gno.land,
https://gno.land, each serving `/r/<ns>/<name>` with Content, Source and
Actions tabs; the Actions tab writes the `gnokey maketx call` line for any
exported crossing function.
