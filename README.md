# ocpp-lab

**An OCPP 1.6J charge point fleet emulator** — spin up a whole fleet of
simulated EV chargers, connect them to any CSMS, script realistic charging
sessions and inject the failures real hardware throws at you.

Built to test CSMS platforms the way they will actually be hit: AC stations
with per-phase currents (and **no SoC** — a Type 2 cable has no data link to
the car), DC fast chargers that **report SoC and taper above 80%**, offline
stations that queue `StopTransaction` and flush it on reconnect, and brutal
TCP kills with no goodbye frame.

## Quick start

```bash
go build ./cmd/ocpp-lab

# 1. Declare your fleet (nouns): see fleet.example.yaml
cp fleet.example.yaml fleet.yaml   # point csms: at your platform

# 2. Run it — every station boots and connects
./ocpp-lab serve --fleet fleet.yaml

# 3. Drive it (verbs) — CLI or plain curl, same API
./ocpp-lab status
./ocpp-lab plug   SIM-DC-001/1
./ocpp-lab charge SIM-DC-001/1        # MeterValues with SoC start flowing
./ocpp-lab kill   SIM-AC-001          # TCP drop, no Close frame
./ocpp-lab offline SIM-DC-001         # sessions keep running, messages queue
./ocpp-lab online  SIM-DC-001         # queue flushes, in order
```

## Design

* **Nouns in YAML, verbs in the API.** The fleet file is declarative desired
  state (Kubernetes ConfigMap-friendly); runtime actions go through the REST
  API, and the CLI is a thin client of it — everything you can do by hand,
  CI can do with curl.
* **Fidelity to the spec.** OCPP-J framing, message shapes, enums and the
  inconvenient behaviors (store-and-forward while offline, `ConnectionTimeOut`)
  follow the OCPP 1.6 specification. When the spec and convenience disagree,
  the spec wins.
* **Physics included.** Sessions run against a simulated EV battery: DC power
  tapers as SoC climbs, AC is capped by the car's onboard charger, and
  `SetChargingProfile` limits are obeyed immediately — which is how you test
  load balancing without a single real charger.
* **Chaos is a feature.** `kill` (no Close frame), `offline`/`online`,
  `fault` with any OCPP error code. The happy path is the least interesting
  thing this tool simulates.

## v1 scope

OCPP **1.6J only**. Implemented: `BootNotification`, `Heartbeat`,
`StatusNotification`, `StartTransaction`, `StopTransaction`, `MeterValues`
(AC: per-phase `Current.Import`; DC: `SoC`, `Voltage`, `Current.Import`),
and inbound `RemoteStartTransaction`, `RemoteStopTransaction`, `Reset`,
`UnlockConnector`, `ChangeAvailability`, `TriggerMessage`,
`Get/ChangeConfiguration`, `SetChargingProfile`, `ClearChargingProfile`.
Everything else answers a proper `NotImplemented` CALLERROR.

OCPP 2.0.1 is a planned major, not a v1 stretch goal.

## Container

```bash
docker build -t ocpp-lab .
docker run -v $PWD/fleet.yaml:/fleet.yaml -p 8887:8887 ocpp-lab serve --fleet /fleet.yaml
```

See [AGENTS.md](AGENTS.md) for the contributor guide — starting with the
mandatory language policy (everything in English) and the engineering rules.
