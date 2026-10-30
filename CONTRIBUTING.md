# Contributing

Thanks for wanting to help.

This project is a fork of [Crush](https://github.com/charmbracelet/crush) by
Charmbracelet, Inc. Its primary licence is the GNO Network General Public
License, version 6, with Additional Terms (Strong Attribution), in
[LICENSE.md](./LICENSE.md), and so is every contribution to it. The files that
came from Crush remain Charmbracelet's, under FSL-1.1-MIT, reproduced at the
end of that file; `internal/gnovm/`, the GnoVM machine subsystem, carries its
own copy of the same License in
[internal/gnovm/LICENSE.md](./internal/gnovm/LICENSE.md).

## Before you open a pull request

- Build and test: `go build ./...` and `go test ./...`.
- Format with `gofumpt` (`gofmt` is not enough; the linter checks it).
- Write a test that fails before your change and passes after it.
- Keep the commit message about what changes and why, in plain sentences.

See [AGENTS.md](./AGENTS.md) for the conventions in more detail.

## Contributor Licence Agreement

By submitting a contribution to this repository — opening a pull request,
sending a patch, or any other form of submission — you agree to the terms
below for that contribution and every later one, unless you say otherwise
in writing at the time you submit it.

**Maintainer** means Jae Kwon, the maintainer of this fork, and their
successors and assigns.

**Charm** means Charmbracelet, Inc., the author of the upstream Crush
project this fork is derived from.

**Contribution** means any original work of authorship you intentionally
submit to this repository, including code, documentation and any
modification of or addition to existing work, except anything you clearly
mark at the time of submission as "Not a Contribution".

### 1. Copyright licence

You grant the Maintainer, **and separately grant Charm**, each a
perpetual, worldwide, non-exclusive, no-charge, royalty-free, irrevocable
copyright licence to reproduce, prepare derivative works of, publicly
display, publicly perform, sublicense and distribute your Contribution and
such derivative works.

Both grants are of the same scope, and each stands on its own: Charm's
grant is made directly to Charm, which is an intended beneficiary of this
agreement and may rely on it, whether or not the Maintainer ever sends
your Contribution upstream. The purpose of granting Charm the same rights
is so that work done here can flow back to Crush without having to come
back to you for permission.

**Exception for `internal/gnovm/`.** For the parts of a Contribution under
`internal/gnovm/` submitted on or after 29 October 2026, you make only the
grants to the Maintainer, in this section and in section 2; the separate
grants to Charm do not extend to them, whatever an earlier agreement under
this file said. The rest of a Contribution that also changes other files is
under the usual terms. That subsystem is not part of Crush and is licensed
under different terms.

### 2. Patent licence

You grant the Maintainer, and separately grant Charm, each a perpetual,
worldwide, non-exclusive, no-charge, royalty-free, irrevocable (except as
stated in this section) patent licence to make, have made, use, offer to
sell, sell, import and otherwise transfer the work, where that licence
applies only to those patent claims you can licence that are necessarily
infringed by your Contribution alone, or by your Contribution combined
with the work it was submitted to.

If you or your organisation starts patent litigation against anyone
alleging that the work, or a Contribution to it, infringes a patent, the
patent licences granted under this agreement to the party you sue end on
the day that litigation is filed.

### 3. What you keep

These licences are non-exclusive. You keep all right, title and interest
in your Contribution, and you may use it for anything else you like.
Nothing here assigns your copyright to anyone.

### 4. What you are promising

- You are legally entitled to grant these licences.
- If your employer has rights to work you create, you have their
  permission to contribute, or they have waived those rights, or they
  have entered into a separate agreement covering your contributions. If
  you are contributing on behalf of a company, you are authorised to do
  so on its behalf.
- To the best of your knowledge, your Contribution is your own original
  creation and does not infringe or misappropriate anyone else's rights.
- If you want to submit work that is not your own creation, submit it
  separately, say where it came from, and name any licence, patent or
  trademark restriction you are aware of, marking it "Submitted on behalf
  of a third party: [name]".
- If you later learn that something above is not accurate, you will say
  so.

### 5. No warranty, no obligation to support

You provide your Contribution as is, without warranties or conditions of
any kind, express or implied, except for what you promise in section 4.
You are not expected to support your Contribution, though you are welcome
to.

## What this agreement does not do

- It does not change the licence on the code. This project's primary licence
  is the GNO Network General Public License, version 6, with Additional Terms
  (Strong Attribution); the files that came from Crush stay under
  FSL-1.1-MIT; and your Contribution is distributed under the licence of the
  part it changes.
- It gives neither the Maintainer nor you any rights to Crush beyond what
  FSL-1.1-MIT already grants, and no rights at all to Charm's trademarks,
  trade names or product names.
- Charm runs its own contributor process for the upstream project. This
  agreement means Charm already holds the rights it would need to accept
  work that originated here, except work under `internal/gnovm/` (see
  section 1); it does not oblige Charm to accept anything,
  or replace any signature Charm asks for in its own repository. Charm's
  own agreement is at [CLA.md](./CLA.md).
