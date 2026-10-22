---
name: gno
description: Use for anything Gno or gno.land - .gno files, gnomod.toml, realms and pure packages, writing, testing or reviewing smart contracts, gnodev, gnoweb, gnokey, deploying or calling realms, or querying a gno.land chain.
---

# Gno and gno.land

Gno is a deterministic, interpreted Go for smart contracts; gno.land is the
chain that runs it. A **realm** (`gno.land/r/...`) is a contract with state:
its package-level variables persist between transactions. A **pure package**
(`gno.land/p/...`) is a stateless library. Code reaches the chain through an
`addpkg` transaction and is immutable there: to upgrade, deploy a new path
(`.../v2`); only a package with `private = true` in its gnomod.toml may be
re-uploaded in place.

A Gno project has a `gnomod.toml` (older: `gno.mod`), a `gnowork.toml`, or
`.gno` files. The tools are `gno` (fmt, lint, test, doc, run, mod), `gnokey`
(keys, transactions, queries) and `gnodev` (a local chain with hot reload and
gnoweb). `gno version` says whether they are installed; `gno env GNOROOT`
names the gno checkout the tools use, whose `docs/` and `examples/` are the
reference. The docs are also at https://docs.gno.land.

## Writing Gno

- Go 1.17 syntax: no generics, goroutines, channels, `select`, `unsafe`, `os`
  or `net`. `any` works. Everything is deterministic.
- Imports are `gno.land/p/...`, `gno.land/r/...` or a stdlib name; never
  `github.com/...`. A realm may import realms and pure packages; a pure
  package imports only pure packages; nothing imports `/e/`.
- `package <name>` equals the last path element: `gno.land/r/dev/counter`
  is `package counter`; under a version suffix, `gno.land/p/nt/avl/v0` is
  still `package avl`. The last element is lowercase letters, digits and
  underscores, starting with a letter, two characters or more.
- Create the module file with `gno mod init gno.land/r/<ns>/<name>`:

  ```toml
  module = "gno.land/r/dev/counter"
  gno = "0.9"
  ```

  Deploy to the path in `module`. `private = true` makes a package
  re-uploadable and unimportable (a user's home realm, say).
- State is the realm's package-level variables; nothing else persists.
  Keep growing data in an `avl.Tree` (`gno.land/p/nt/avl/v0`) rather than
  a map. Other `gno.land/p/nt/<name>/v0` libraries: `ufmt` (formatting),
  `seqid` (ordered IDs), `ownable` (an owner), `grc20` and `grc721`
  (tokens), `mux` (routes in `Render`), `markdown/sanitize` (escaping),
  `uassert` and `urequire` (tests), `addrset`, `testutils`. Read one with
  `gno doc gno.land/p/nt/avl/v0`.
- Builtin types: `address` (a `g1...` bech32 string) and `realm` (a caller
  identity: `Address()`, `PkgPath()`, `Previous()`, `IsUser()`,
  `IsUserCall()`, `IsUserRun()`, `IsCode()`, `IsCurrent()`).
- `func Render(path string) string` returns markdown that gnoweb shows at
  `/r/<ns>/<name>:<path>`. `path` and anything users stored are attacker
  input: pass them through `sanitize.InlineText` before writing them out.
- `panic` to reject a call; a transaction has no error return. Say what
  happened with `chain.Emit("Name", "key", value, ...)`.
- Chain stdlibs (`gno doc <pkg>`): `chain` (Emit, PackageAddress, Coin,
  Coins); `chain/banker` (`NewBanker(banker.BankerTypeRealmSend, cur)`,
  `NewReadonlyBanker()`, `GetCoin(addr, denom)` for one balance, never
  `GetCoins(addr).AmountOf(denom)`, `SendCoins`, `IssueCoin`);
  `chain/runtime` (ChainID, ChainHeight, ChainDomain, AssertOriginCall);
  `chain/runtime/unsafe` (OriginCaller, OriginSend, CurrentRealm,
  PreviousRealm: they walk the frames, for `init()` or where there is no
  `cur`); `testing` (NewUserRealm, NewCodeRealm, SetRealm, SetOriginCaller,
  SetOriginSend, IssueCoins, SkipHeights).

## Crossing: who is calling

- A function a transaction can call takes `cur realm` first:
  `func Increment(cur realm) int`. Only realms declare such functions, and
  `maketx call` reaches only them.
- Calling one from another realm, a test or a script:
  `counter.Increment(cross(cur))`. Within the same realm, pass `cur` on:
  `helper(cur, x)`.
- The caller is `cur.Previous()`: `caller := cur.Previous().Address()`.
  Compare it with stored addresses for access control. Never take the
  caller as an `address` parameter, and never read
  `unsafe.PreviousRealm()` beside a `cur realm` parameter.
- `rlm.IsCurrent()` checks a realm value a caller hands you as an argument
  (`rlm` by convention) before you trust it; your own `cur` needs no check.
- Never store a `realm` value; store `.Address()` or `.PkgPath()`.
- Taking payment: `cur.Previous().IsUserCall()` (not `IsUser()`, which also
  passes `maketx run` scripts that may have spent the coins) together with
  `unsafe.OriginSend()`; keep the two checks side by side.
- Never return a pointer to internal state, nor a `/p/`-typed pointer with
  mutating methods such as `*avl.Tree`: the caller can then write under your
  realm's authority. Return values or read-only accessors.
- The deployer is `unsafe.PreviousRealm().Address()` during `init()` only;
  save it there if you need it later.

Before writing or reviewing a realm that holds assets, has an admin, or
takes callbacks or interfaces, view
`crash://skills/gno/references/security-review.md`.

## Test, lint, run

From the package directory:

```sh
gno fmt -w .       # canonical formatting (gofmt also works)
gno lint .         # unused imports and variables, misuse of cross and realm
gno test -v .      # runs *_test.gno; -run Name; -print-runtime-metrics for gas
```

- A test that calls a crossing function takes `cur realm` first:
  `func TestIncrement(cur realm, t *testing.T) { Increment(cross(cur)) }`.
  Others are plain: `func TestRender(t *testing.T)`. Test helpers must not
  be called `init`.
- Be someone else: `testing.SetRealm(testing.NewUserRealm(addr))` or
  `testing.SetRealm(testing.NewCodeRealm("gno.land/r/other"))`; attach
  coins with `testing.SetOriginSend(chain.Coins{{"ugnot", 100}})`.
- `gno run -expr 'println(Add(2, 3))' .` evaluates a non-crossing function
  without a chain.
- Filetests (`filetests/*_filetest.gno`, `package main`, expectations in
  `// Output:` comments at the bottom) are golden tests:
  `gno test -run _filetest.gno .`; `-update-golden-tests` rewrites them.
- Tests run on a mocked VM with the stdlibs and examples of the local gno
  install; no chain is touched.

## Local chain with gnodev

Start it as a background job from the package directory and leave it
running while you work:

```sh
gnodev .           # loads the package at its gnomod.toml path; name more dirs to load them too
```

It is up when the job output says `--- READY`. Then: gnoweb at
http://127.0.0.1:8888 (fetch `http://127.0.0.1:8888/r/dev/counter` to see
`Render`), RPC at `127.0.0.1:26657`, chain id `dev`. Every key in the local
keybase is premined, and the `devtest` key
(`g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5`, a public mnemonic, never for
real funds) is imported for you. Saving a `.gno` file reloads the chain and
replays its transactions; load errors show in the job output. `-no-web`
drops gnoweb; `-node-rpc-listener` and `-web-listener` move the ports.

## Reading a chain (free, no key)

```sh
gnokey query vm/qrender -data "gno.land/r/dev/counter:" -remote 127.0.0.1:26657            # Render(""); "<pkgpath>:<path>"
gnokey query vm/qeval -data 'gno.land/r/dev/counter.Render("")' -remote 127.0.0.1:26657    # any non-crossing expression
gnokey query vm/qfuncs -data gno.land/r/dev/counter -remote 127.0.0.1:26657                 # exported functions and signatures
gnokey query vm/qfile -data gno.land/r/dev/counter -remote 127.0.0.1:26657                  # files; add /counter.gno for one
gnokey query bank/balances/g1... -remote 127.0.0.1:26657                                     # balances; auth/accounts/g1... has number and sequence
```

## Transactions: deploy and call

Signing needs the key's password. Use the `gnokey` tool: it shows the user
the exact command, takes the password in a masked prompt and hands it to
gnokey; you never see it. Never ask for a password, in chat or through the
question tool, and never try to sign through bash: no
`-insecure-password-stdin` (bash refuses it), no pipes, heredocs,
`/dev/stdin`, `expect` or `script`. If the `gnokey` tool is not in your
tool list, you cannot sign in this run: reply with the exact `gnokey`
command for the user to run in their own terminal, and stop there.

Before a deploy: `gno fmt`, `gno lint` and `gno test` pass; the security
review is clean; `gnokey list` shows the key; the path is under a namespace
the key owns, which today is its own address
(`gno.land/{p,r}/<g1 address>/...`); on a shared network the account holds
GNOT (`bank/balances`; https://faucet.gno.land funds the test networks).

```sh
gnokey maketx addpkg -pkgpath gno.land/r/<ns>/counter -pkgdir . \
  -gas-fee 1000000ugnot -gas-wanted 20000000 -chainid dev -remote 127.0.0.1:26657 devtest
gnokey maketx call -pkgpath gno.land/r/<ns>/counter -func Increment \
  -gas-fee 1000000ugnot -gas-wanted 2000000 -chainid dev -remote 127.0.0.1:26657 devtest
gnokey maketx send -to g1... -send 100ugnot \
  -gas-fee 1000000ugnot -gas-wanted 2000000 -chainid dev -remote 127.0.0.1:26657 devtest
gnokey maketx run -gas-fee 1000000ugnot -gas-wanted 20000000 -chainid dev -remote 127.0.0.1:26657 devtest script.gno
```

- The key name comes last. `-gas-wanted` caps the gas (20M for addpkg, 2M
  for a call is usual); `-gas-fee` is the fee; `-args` passes one argument
  per flag; `-send 100ugnot` attaches coins; `-max-deposit` caps the storage
  deposit a deploy locks. A `run` script is `package main` with
  `func main(cur realm)`.
- `-broadcast=false` prints the unsigned transaction (no password);
  `-simulate only` signs and simulates without broadcasting.
- Success prints `OK!`, `GAS USED`, `HEIGHT`, `EVENTS` and `TX HASH`
  (`PKGPATH` too for addpkg); a call's return value comes first, as
  `(1 int)`. Verify a deploy with `vm/qfile`, `vm/qrender` and the gnoweb
  page.

| Network | chain id | RPC | gnoweb |
|---|---|---|---|
| gnodev | `dev` | `127.0.0.1:26657` | http://127.0.0.1:8888 |
| staging (tracks master, may reset) | `staging` | `https://rpc.staging.gno.land:443` | https://staging.gno.land |
| onyx testnet | `onyx-1` | `https://rpc.onyx.testnets.gno.land:443` | https://onyx.testnets.gno.land |
| mainnet | `gnoland-1` | `https://rpc.gno.land:443` | https://gno.land |

Mainnet spends real GNOT and has no faucet: show the user the full command
and get a clear yes before signing.

## More

- `crash://skills/gno/references/security-review.md`: the review checklist
  and the cases behind it.
- `crash://skills/gno/references/toolchain.md`: gno, gnokey and gnodev
  flags, query paths, the airgapped signing flow.
- `$GNOROOT/docs/resources/`: `effective-gno.md`, `gno-interrealm-v2.md`,
  `gno-security-guide.md`, `gno-testing.md`, `gnodev.md`,
  `gnoland-networks.md`; `$GNOROOT/examples/gno.land/{p,r}` for working
  code.
