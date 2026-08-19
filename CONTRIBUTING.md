# Contributing to ocpp-lab

Thanks for considering a contribution! This project has a small set of firm
rules — they keep a public protocol tool trustworthy.

## The non-negotiables

1. **English everywhere.** Code, comments, commit messages, branch names, PR
   titles and bodies, issues, docs. A single non-English identifier is a
   review blocker. (Full policy in [AGENTS.md](AGENTS.md) §1.)
2. **The spec wins.** Message shapes, enums and behaviors follow the official
   OCPP 1.6 specification and its JSON schemas. If a change makes the emulator
   friendlier but less faithful, it is a regression. Cite the spec section in
   the PR when behavior is involved.
3. **Physical honesty.** AC stations must never report SoC; DC taper, per-phase
   currents and onboard-charger limits stay realistic. An emulator that flatters
   the CSMS under test is worse than none.
4. **Tests are part of the change.** New message type → round-trip test. New
   state transition → state machine test. New behavior → e2e test against the
   embedded fake CSMS. Bug fix → the regression test that would have caught it.

## Workflow

* Branch from `main`, conventional commit style (`feat:`, `fix:`, `docs:`,
  `test:`, `chore:`).
* `go vet ./...` and `go test ./...` must be green locally before opening a PR.
* One concern per PR. Small PRs review fast.
* API changes update README and, when they add capability, `fleet.example.yaml`.

## What we will say no to

* OCPP 2.0.1 patches into the 1.6 codepaths — 2.0.1 is a planned major with its
  own package, not a flag.
* Vendor-specific quirks presented as defaults. Quirk emulation is welcome, but
  behind explicit configuration, documented as the quirk it is.
* Anything that requires credentials or endpoints of a specific CSMS operator
  in this repository.

## Reporting bugs

Open an issue with the fleet file (redact your CSMS URL), the action sequence,
and what the CSMS saw vs. what the spec says it should have seen. Protocol
traces (`←`/`→` frames) make bugs trivially reproducible.
