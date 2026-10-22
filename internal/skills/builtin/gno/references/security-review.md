# Reviewing a Gno realm

A checklist for a realm that holds assets, has an owner or admin, charges
for calls, or accepts callbacks and interfaces. Distilled from
`docs/resources/gno-ai-contract-review.md` and `gno-security-guide.md` in
the gno repository; read those for the mechanics (borrow rules, readonly
taint). Go through every item for every exported function.

## Checklist

- [ ] Every function that must know its caller takes `cur realm` and
      reads `cur.Previous()`; no caller comes in as an `address` argument.
- [ ] No `chain/runtime/unsafe` import in a file that also declares
      `cur realm` parameters. (`unsafe.PreviousRealm()` walks the frames
      and answers for wherever it is called; `cur.Previous()` names the
      realm that was current when `cur` was minted.)
- [ ] `IsCurrent()` is called only on a realm value a caller passed in
      (`rlm`), before any identity is read from it. A guard on your own
      `cur` is always true and refuses nobody.
- [ ] Payment guards use `cur.Previous().IsUserCall()` and check
      `unsafe.OriginSend()`, together. `IsUser()` also accepts `maketx run`
      scripts, which can spend the sent coins before calling you.
- [ ] No exported function returns a pointer to internal mutable state.
- [ ] No exported function returns a pointer to a `/p/` type that has any
      exported mutating method (`*avl.Tree` has `Set` and `Remove`): the
      caller invokes the method and the write commits under your realm's
      authority. Return values or narrow read-only views.
- [ ] No exported field of a `/p/` struct you hand out is itself a pointer
      to such a type.
- [ ] No function accepts a `func(...)` whose parameters are `/p/`-declared
      types and then calls it: a top-level `/p/` function supplied by the
      caller runs with your authority. Type callbacks with your own `/r/`
      types.
- [ ] Interface parameters from outside are checked for their concrete
      type (`t.(*Mine)` or an `IsCanonicalX` predicate) before use;
      embedding-based seals are bypassable.
- [ ] No `realm`-typed value is stored in a package variable, struct
      field, map, slice or closure; the VM refuses it at commit anyway.
- [ ] A `/p/` type held in realm state whose methods take callbacks
      (`Iterate(func(*Node) bool)`) is unexported and never returned by
      pointer.
- [ ] Types that hold sensitive state are declared in this realm, not in a
      shared `/p/` package.
- [ ] `Render` escapes path segments, keys and stored user text
      (`sanitize.InlineText`, or at least the `|` in table cells); a raw
      `path` in a heading or link is injection.
- [ ] One balance is read with `banker.GetCoin(addr, denom)`, not
      `GetCoins(addr).AmountOf(denom)`: anyone can send an address new
      denoms, so `GetCoins` on a caller-supplied address costs what a third
      party decides. Exceptions: the denom itself is unvalidated caller
      input (`GetCoin` panics on a malformed one, so a `Render` query
      parameter must not reach it), or the function already holds the
      `GetCoins` result.
- [ ] The deployer or admin address is captured in `init()` from
      `unsafe.PreviousRealm().Address()` if it is needed later; after
      `init` it is gone.

## Shapes to recognise

```go
// Caller identity
func AdminOnly(cur realm) {
    if cur.Previous().Address() != admin { panic("permission denied") }
}

// A realm value the caller filled in
func Act(rlm realm) {
    if !rlm.IsCurrent() { panic("stale realm") }
    who := rlm.Previous().Address()
}

// Payment
func Buy(cur realm) {
    if !cur.Previous().IsUserCall() { panic("call directly with maketx call") }
    if unsafe.OriginSend().AmountOf("ugnot") != price { panic("wrong amount") }
}

// State behind accessors, never by pointer
var store = avl.NewTree()
func Get(key string) (any, bool) { return store.Get(key) }   // fine
func Store() *avl.Tree { return store }                        // write-authority leak
```

## When to say no

Flag, with the line, anything on the list above; it is a defect, not a
style point. Where a fix needs a design choice (which type to declare in
the realm, how to restructure a callback), say what the choice is rather
than guessing. The pattern harness in `misc/audit-pattern-harness/` of the
gno repository has a vulnerable and a fixed fixture for each family; its
text matching reports false positives and negatives, so a clean run is a
smoke test, not a verdict.
