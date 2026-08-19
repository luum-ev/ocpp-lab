# ocpp-lab — Agent & Contributor Guide

**ocpp-lab** is an open-source OCPP charge point fleet emulator: a controller that
spins up N simulated charge points (each with 1..n connectors), connects them to
any CSMS over OCPP-J, and lets you script realistic charging sessions and inject
failures — from a YAML fleet file, a REST API, or the CLI.

## 1. Language policy — MANDATORY

**Everything in this repository is in English**: code, identifiers, comments,
commit messages, branch names, PR titles and bodies, issues, docs, YAML keys,
API routes, error messages. No exceptions. This is a public-facing project;
a single non-English identifier is a review blocker.

## 2. The one rule above all: fidelity to the spec

This emulator exists to validate real CSMS implementations. An emulator that is
"almost OCPP" produces platforms that are "almost working".

* v1 targets **OCPP 1.6J only** (JSON over WebSocket). OCPP 2.0.1 is a future
  major, not a v1 stretch goal.
* Message shapes follow the official OCPP 1.6 JSON schemas — field names, enums,
  optionality. When in doubt, the spec wins over convenience.
* Behavior follows the spec too, including the inconvenient parts: offline
  transaction queueing (store-and-forward of `StopTransaction`/`MeterValues`),
  `ConnectionTimeOut`, message ordering, idempotent retries with the same
  `messageId`.
* **Physical fidelity matters as much as protocol fidelity**: AC stations do NOT
  report SoC (Type 2 has no data link to the car); DC stations DO (CCS talks to
  the BMS). Per-phase currents on AC, DC voltage/current and battery taper on DC.

## 3. Architecture: nouns in YAML, verbs in the API

* **The fleet file (`fleet.yaml`) is declarative state** — which stations exist,
  their model, connector count, AC/DC, power. The process boots the whole fleet
  from it (Kubernetes ConfigMap-friendly).
* **Runtime actions are events, not state** — plug/unplug a cable, start a
  charging scenario, kill a station brutally, take it offline. They go through
  the REST API; the CLI and any future web UI are thin clients of that same API.
  If the UI can do something the API can't, that is a bug.
* **Chaos is a feature, not an afterthought**: brutal TCP drops (no Close frame),
  network flaps mid-`StopTransaction`, duplicate `StartTransaction`, clock skew.
  The happy path is the least interesting thing this tool simulates.

## 4. Layout

```
cmd/ocpp-lab/     CLI entrypoint (serve + client subcommands)
internal/ocpp/    OCPP-J framing (CALL/CALLRESULT/CALLERROR) + 1.6 message types
internal/station/ simulated charge point: WS client, connector state machine,
                  metering engine (AC/DC), offline queue
internal/fleet/   fleet.yaml loading and station lifecycle
internal/api/     REST control plane
```

## 5. Engineering rules

* Go, standard library first; dependencies must earn their place
  (`gorilla/websocket` and `yaml.v3` have).
* Every state machine transition is tested. Every OCPP message type has a
  round-trip (marshal/unmarshal) test.
* `go vet` and `go test ./...` green before any commit.
* No Luum-specific endpoints, credentials or defaults in this repository —
  private fleet configs live elsewhere. This repo stays generic.
* Conventional commit style: `feat:`, `fix:`, `docs:`, `test:`, `chore:`.

## 6. What v1 deliberately does NOT do

OCPP 2.0.1 · ISO 15118 / Plug&Charge · smart-charging *optimization* (we obey
`SetChargingProfile`; we don't compute profiles) · a rich web UI (the API comes
first; a thin UI can follow) · security profiles 2/3 (TLS client certs) — v1
speaks `ws://` and `wss://` with basic auth only.
