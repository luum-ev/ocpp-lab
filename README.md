# ocpp-lab

**An OCPP 1.6J charge point fleet emulator** — spin up a whole fleet of
simulated EV chargers, connect them to any CSMS, script realistic charging
sessions and inject the failures real hardware throws at you.

Built to test CSMS platforms the way they will actually be hit: AC stations
with per-phase currents (and **no SoC** — a Type 2 cable has no data link to
the car), DC fast chargers that **report SoC and taper above 80%**, offline
stations that queue `StopTransaction` and flush it on reconnect, and brutal
TCP kills with no goodbye frame.

## Status

Pre-release. There is no tagged release yet, and the Helm chart has never
been published. Build the binary or the container image from a checkout, as
shown below.

## Quick start

Requires Go 1.22 or newer.

```bash
go build ./cmd/ocpp-lab

# 1. Declare your fleet (nouns): see fleet.example.yaml
cp fleet.example.yaml fleet.yaml   # point csms: at your platform

# 2. Run it: every station boots and connects
./ocpp-lab serve --fleet fleet.yaml
```

Then drive it (verbs) from another terminal, with the CLI or plain curl
against the same API. Each scenario below works on the example fleet, and they
can run one after the other.

```bash
./ocpp-lab status

# A DC session that survives an outage: MeterValues carry SoC, keep queueing
# while offline, and flush in order on reconnect.
./ocpp-lab plug    SIM-DC-001/1
./ocpp-lab charge  SIM-DC-001/1
./ocpp-lab offline SIM-DC-001
./ocpp-lab online  SIM-DC-001
./ocpp-lab stop    SIM-DC-001/1        # StopTransaction, reason Local
./ocpp-lab unplug  SIM-DC-001/1

# RFID tap: Authorize goes to the CSMS and the transaction starts only if the
# CSMS accepts the tag. Then the driver pulls the cable from the CAR, which
# stops the transaction (EVDisconnected) and frees the connector.
./ocpp-lab plug SIM-AC-001/1
curl -X POST localhost:8887/stations/SIM-AC-001/connectors/1/rfid -d '{"idTag":"TAG-01"}'
curl -X POST localhost:8887/stations/SIM-AC-001/connectors/1/ev-disconnect

# The car stops drawing energy but the transaction stays open (SuspendedEV:
# 0 W, flat register, cable locked): the idle time after a charge. Then a
# stop with any OCPP 1.6 reason.
./ocpp-lab plug   SIM-AC-001/2
./ocpp-lab charge SIM-AC-001/2
curl -X POST localhost:8887/stations/SIM-AC-001/connectors/2/ev-suspend
curl -X POST localhost:8887/stations/SIM-AC-001/connectors/2/stop -d '{"reason":"PowerLoss"}'
./ocpp-lab unplug SIM-AC-001/2

# Chaos: a TCP drop with no Close frame (the station reconnects and boots
# again), then a connector fault.
./ocpp-lab kill SIM-AC-001
curl -X POST localhost:8887/stations/SIM-DC-001/connectors/2/fault -d '{"errorCode":"GroundFailure"}'
```

A faulted connector stays `Faulted`: the API has no way back. A CSMS
`ChangeAvailability` with `Operative` returns it to `Available`, and so does a
restart of `ocpp-lab`.

## Control API

The REST API listens on `:8887`. The CLI and the web UI are thin clients of it.

| Route | Body | What it does |
| :--- | :--- | :--- |
| `GET /stations` | | Snapshot of every station and connector (state, cable, session, reservation). |
| `GET /healthz` | | Liveness. |
| `POST /stations/{id}/connectors/{n}/plug` | | Plug the cable. |
| `POST /stations/{id}/connectors/{n}/unplug` | | Unplug it. Refused while the cable is locked (`Charging`, `SuspendedEV`). |
| `POST /stations/{id}/connectors/{n}/charge` | optional `{"idTag":"...","battery":{...}}` | Start a local transaction. `idTag` defaults to `ocpp-lab`. `battery` replaces the station's simulated EV for this session, with the fleet file's keys: `capacityKwh`, `socPercent`, `targetSoc` (100 when 0), `maxAcKw`, `maxDcKw`. |
| `POST /stations/{id}/connectors/{n}/rfid` | `{"idTag":"..."}` (required) | Card tap: `Authorize`, then the start only if the CSMS accepts. Answers `202`; the outcome shows in the snapshot. |
| `POST /stations/{id}/connectors/{n}/ev-disconnect` | | The driver pulls the cable from the car: stop with `EVDisconnected` and unplug. |
| `POST /stations/{id}/connectors/{n}/ev-suspend` | | The car stops drawing energy; the transaction stays open. |
| `POST /stations/{id}/connectors/{n}/stop` | optional `{"reason":"..."}` | Stop with an OCPP 1.6 `StopTransaction` reason. Empty or non-JSON body means `Local`; a reason outside the enum is `400`. |
| `POST /stations/{id}/connectors/{n}/fault` | optional `{"errorCode":"..."}` | `StatusNotification` `Faulted` with that error code (default `OtherError`). The code is sent as given, so use an OCPP 1.6 `ChargePointErrorCode`. A running transaction stops with reason `Other`. |
| `POST /stations/{id}/kill` | | Drop the TCP connection without a Close frame. |
| `POST /stations/{id}/offline`, `.../online` | | Take the station off the network and back. |

An action that does not fit the current state (no cable, no transaction,
cable locked) answers `409` with `{"error": "..."}`.

**The API has no authentication.** Anyone who can reach `:8887` can drive the
fleet. Keep it on a trusted network, and bind it to the loopback interface when
you publish a container port (`-p 127.0.0.1:8887:8887`).

## Web UI

The binary ships a **thin web UI** at `http://localhost:8887/`: the whole fleet
at a glance, refreshed every 2 seconds, with the state, cable, energy and (DC
only) SoC of each connector. It is a client of the same REST API the CLI uses
(one embedded file, no build step), so anything you click, CI can replay with
curl.

What it has:

* per connector: `plug`, `unplug`, `charge`, `stop` and `fault`;
* per station: `go offline` / `go online` and `kill`.

What it does not have: the RFID tap, `ev-disconnect` and `ev-suspend`, a stop
reason (its `stop` is always `Local`), a fault error code (always
`OtherError`), and a custom `idTag` or battery for `charge`. Use the API for
those.

## Design

* **Nouns in YAML, verbs in the API.** The fleet file is declarative desired
  state (Kubernetes ConfigMap-friendly); runtime actions go through the REST
  API, and the CLI is a thin client of it — everything you can do by hand,
  CI can do with curl.
* **Fidelity to the spec.** OCPP-J framing, message shapes and enums follow
  the OCPP 1.6 specification, and so do the inconvenient behaviors that are
  implemented, such as store-and-forward while offline. When the spec and
  convenience disagree, the spec wins. The v1 scope below says what is
  implemented and what is partial.
* **Physics included.** Sessions run against a simulated EV battery: DC power
  tapers as SoC climbs, AC is capped by the car's onboard charger, and
  `SetChargingProfile` limits are obeyed immediately — which is how you test
  load balancing without a single real charger.
* **Chaos is a feature.** `kill` (no Close frame), `offline`/`online`,
  `fault` with any OCPP error code. The happy path is the least interesting
  thing this tool simulates.

## v1 scope

OCPP **1.6J only**. OCPP 2.0.1 is a planned major, not a v1 stretch goal.

Sent by the station: `BootNotification`, `Heartbeat`, `StatusNotification`,
`Authorize` (on an RFID tap), `StartTransaction`, `StopTransaction` and
`MeterValues` (AC: per-phase `Current.Import`; DC: `SoC`, `Voltage`,
`Current.Import`; periodic `Sample.Periodic` and clock-aligned `Sample.Clock`).

Handled when the CSMS sends them: `RemoteStartTransaction`,
`RemoteStopTransaction`, `Reset`, `UnlockConnector`, `ChangeAvailability`,
`GetConfiguration`, `ChangeConfiguration`, `ReserveNow`, `CancelReservation`,
and these, which are partial:

* `TriggerMessage` answers `Accepted` to every request, but only sends
  `Heartbeat`, `StatusNotification` and `BootNotification`. Any other
  requested message is not sent.
* `SetChargingProfile` obeys only the limit of the first period of the
  schedule, from the moment it arrives. Later periods, `startPeriod`, stack
  levels and purposes are ignored, and a profile for `connectorId` 0 has no
  effect.
* `ClearChargingProfile` clears every profile and answers `Accepted`; its
  filters (`id`, `connectorId`, `chargingProfilePurpose`, `stackLevel`) are
  ignored.
* `ChangeAvailability` answers `Accepted` and changes the connectors with no
  transaction. A connector with a transaction keeps its state; the station
  never answers `Scheduled`.

Everything else answers a proper `NotImplemented` CALLERROR.

### Configuration keys

`GetConfiguration` and `ChangeConfiguration` know exactly these keys:

| Key | Default | Accepted values | Effect |
| --- | --- | --- | --- |
| `MeterValueSampleInterval` | the station's `meterValuesS` (30 when unset) | integer, 0 to 86400 | Seconds between periodic `MeterValues` (`Sample.Periodic`) for every running transaction. `0` stops them. |
| `MeterValuesSampledData` | everything the station measures | comma-separated measurands, see below | The measurands periodic samples carry. |
| `ClockAlignedDataInterval` | `0` (off) | integer, 0 to 86400 | `MeterValues` with `Sample.Clock` on wall-clock boundaries, every N seconds from 00:00 UTC, while a transaction is running, in addition to the periodic ones. Each is stamped with its boundary. `0` stops them. |
| `MeterValuesAlignedData` | `Energy.Active.Import.Register` | comma-separated measurands, see below | The measurands clock-aligned samples carry. |
| `HeartbeatInterval` | the station's `heartbeatS` (300 when unset) | integer above 0 | Seconds between `Heartbeat`s, from the next connection on. |
| `NumberOfConnectors` | the station's `connectors` | read-only | |

No other key exists. `ConnectionTimeOut`, for example, is not implemented:
`GetConfiguration` lists it under `unknownKey`, and `ChangeConfiguration`
answers `NotSupported`.

The measurands a station can report depend on its type:

* AC: `Energy.Active.Import.Register`, `Power.Active.Import`, `Current.Import`
  (per phase).
* DC: `Energy.Active.Import.Register`, `Power.Active.Import`, `SoC`, `Voltage`,
  `Current.Import`.

By default periodic samples carry all of them, which is what the emulator
always sent. A CSMS that sets `MeterValuesSampledData` gets exactly the list it
asked for, so SoC is sent only when requested, and only by a DC station.

How `ChangeConfiguration` answers:

* A new interval is `Accepted` and applies at once: the sampling ticker and
  the clock-aligned timer are reset live, with no reboot and no reconnect.
* A value that is not an integer in range is `Rejected`, and the old value stays.
* A measurand list is `Rejected` as a whole when any item is not a measurand
  the station can produce: a typo, `Temperature`, or `SoC` on an AC station (a
  Type 2 cable has no data link to the car). Accepting it would promise data
  that never arrives.
* `NumberOfConnectors` is read-only, so a change is `Rejected`.
* Any other key is `NotSupported`.

`GetConfiguration` with no key returns every key above. With keys, it returns
those and lists the others under `unknownKey`.

The simulated car does not depend on any of this: physics advances on its own
one-second step, so the energy register, the target SoC and reservation expiry
stay right even with sampling turned off.

### Reservations

`ReserveNow` puts the connector in `Reserved` and the station itself refuses a
transaction from any other `idTag` — at the **start**, not at the cable, because
a real station cannot stop a hand from inserting a plug.

Two properties worth knowing when you test a CSMS against this:

* **The station expires its own holds**, on the physics tick. Expiry does not
  depend on the CSMS sending `CancelReservation`, so a network partition never
  leaves a connector held with nobody able to free it.
* **`expiryDate` is mandatory.** A missing or past expiry is `Rejected`. A CSMS
  that omits it would otherwise hold the connector forever and discover its own
  bug in the field.

The full status set is reachable: `Occupied` when a cable is in or someone else
holds it, `Faulted` and `Unavailable` for the states where the connector cannot
serve anyone, and a repeated `reservationId` is treated as a retry — it updates
the hold instead of failing. A status a test can never observe teaches nothing.

The current hold shows up in `GET /stations` under each connector, so a test can
assert on the reservation itself instead of on side effects.

### Security Profile 2 (Basic Auth), optional per station

Give a station an `authorizationKey` in the fleet file and its WebSocket
upgrade carries HTTP Basic Auth — the station `id` as user, the key as
password, as the OCPP 1.6 Security Whitepaper describes. Without it the
station connects as Security Profile 1, with no credential. A refused
handshake is logged with its HTTP status (`401`), so a wrong key does not look
like a TLS problem. The key never appears in `GET /stations`.

The whitepaper sets the key at 16 to 40 characters. ocpp-lab does not enforce
that length: it sends whatever key the fleet file holds, so you can also test
how a CSMS treats a key outside the range.

Use `wss://` for anything that is not your own machine: Basic Auth over plain
`ws://` sends the key in the clear.

Security Profile 3 (TLS client certificates) is not supported.

## Configuration

`ocpp-lab serve` takes its settings from flags, then environment variables,
then defaults:

| Flag | Env var | Default | Meaning |
| :--- | :--- | :--- | :--- |
| `--fleet` | `OCPP_LAB_FLEET` | `fleet.yaml` (the container image sets `/etc/ocpp-lab/fleet.yaml`) | fleet file path |
| `--listen` | `OCPP_LAB_LISTEN` | `:8887` | control API address |
| `--csms` | `OCPP_LAB_CSMS` | none | overrides the fleet file's `csms` URL, so the same fleet file serves every environment |

The client commands (`status`, `plug`, `charge`…) take `--api` (env
`OCPP_LAB_API`, default `http://localhost:8887`), the control API base URL.
Put it after the command's argument, for example
`./ocpp-lab plug SIM-AC-001/1 --api http://sim:8887`.

## Container

Build the image from a checkout. The image on GHCR (`ghcr.io/luum-ev/ocpp-lab`)
is not public yet, so an anonymous pull fails with `401`.

```bash
docker build -t ocpp-lab .

# docker or podman — mount the fleet, point at your CSMS, and keep the
# unauthenticated control API on the loopback interface
docker run --rm -v $PWD/fleet.yaml:/etc/ocpp-lab/fleet.yaml:ro \
  -e OCPP_LAB_CSMS=ws://host.docker.internal:9000/ocpp \
  -p 127.0.0.1:8887:8887 ocpp-lab

curl -s localhost:8887/healthz     # liveness for k8s probes too
```

## Kubernetes (Helm)

The chart lives in [`charts/ocpp-lab`](charts/ocpp-lab). It is meant to be
published as an OCI artifact on release tags, but no release exists yet, so
install it from a checkout. With a local cluster, build the image and load it
into the cluster instead of pulling it:

```bash
docker build -t ocpp-lab:dev .
minikube image load ocpp-lab:dev        # kind: kind load docker-image ocpp-lab:dev

helm install sim charts/ocpp-lab \
  --set image.repository=ocpp-lab --set image.tag=dev --set image.pullPolicy=Never \
  --set csms=ws://my-csms.dev.svc:9000/ocpp

kubectl port-forward svc/sim 8887:8887
./ocpp-lab status   # or curl localhost:8887/stations
```

The fleet is a value (`.Values.fleet`) rendered into a ConfigMap and mounted
at `/etc/ocpp-lab/fleet.yaml`; the CSMS endpoint is `.Values.csms`, injected
as `OCPP_LAB_CSMS` — one image, one chart, any environment. A fleet change
rolls the pod (checksum annotation), and the stations reconnect and boot.

A station's `authorizationKey` is part of the fleet, so it lands in plain text
in that ConfigMap. Anyone who can read ConfigMaps in the namespace can read it:
use test keys only.

Licensed under [Apache-2.0](LICENSE). See [CONTRIBUTING.md](CONTRIBUTING.md)
and [AGENTS.md](AGENTS.md) for the contributor guide — starting with the
mandatory language policy (everything in English) and the engineering rules.
