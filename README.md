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
# RFID tap (Authorize first; starts only if the CSMS accepts) and the
# driver pulling the cable from the CAR mid-session:
curl -X POST localhost:8887/stations/SIM-AC-001/connectors/1/rfid -d '{"idTag":"TAG-01"}'
curl -X POST localhost:8887/stations/SIM-AC-001/connectors/1/ev-disconnect
./ocpp-lab offline SIM-DC-001         # sessions keep running, messages queue
./ocpp-lab online  SIM-DC-001         # queue flushes, in order
```

## Web UI

The binary ships a **thin web UI** at `http://localhost:8887/` — the whole
fleet at a glance, with plug/charge/stop/fault per connector and
kill/offline/online per station. It is deliberately a dumb client of the same
REST API the CLI uses (one file, embedded, no build step): every button is one
REST call, so anything you click, CI can replay with curl.

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

The image is published to GHCR on every merge to `main`
(`ghcr.io/luum-ev/ocpp-lab`, linux/amd64 + arm64). Configuration is
environment-first so the same image runs everywhere:

| Env var | Default | Meaning |
| :--- | :--- | :--- |
| `OCPP_LAB_FLEET` | `/etc/ocpp-lab/fleet.yaml` | fleet file path (mount a volume or a ConfigMap there) |
| `OCPP_LAB_CSMS` | — | overrides the fleet file's `csms` URL — the same fleet file then serves every environment |
| `OCPP_LAB_LISTEN` | `:8887` | control API address |
| `OCPP_LAB_API` | `http://localhost:8887` | base URL used by the CLI client commands |

Flags always win over env vars; env vars win over defaults.

```bash
# docker or podman — mount the fleet, point at your CSMS
docker run -v $PWD/fleet.yaml:/etc/ocpp-lab/fleet.yaml \
  -e OCPP_LAB_CSMS=ws://host.docker.internal:9000/ocpp \
  -p 8887:8887 ghcr.io/luum-ev/ocpp-lab

curl -s localhost:8887/healthz     # liveness for k8s probes too
```

## Kubernetes (Helm)

The chart lives in [`charts/ocpp-lab`](charts/ocpp-lab) and is published as an
**OCI artifact** on every release tag:

```bash
# From the published OCI registry:
helm install sim oci://ghcr.io/luum-ev/charts/ocpp-lab \
  --set csms=ws://my-csms.dev.svc:9000/ocpp

# Or straight from a checkout (minikube-friendly):
helm install sim charts/ocpp-lab --set csms=ws://my-csms.dev.svc:9000/ocpp

kubectl port-forward svc/sim 8887:8887
./ocpp-lab status   # or curl localhost:8887/stations
```

The fleet is a value (`.Values.fleet`) rendered into a ConfigMap and mounted
at `/etc/ocpp-lab/fleet.yaml`; the CSMS endpoint is `.Values.csms`, injected
as `OCPP_LAB_CSMS` — one image, one chart, any environment. A fleet change
rolls the pod (checksum annotation), and the stations reconnect and boot.

Licensed under [Apache-2.0](LICENSE). See [CONTRIBUTING.md](CONTRIBUTING.md)
and [AGENTS.md](AGENTS.md) for the contributor guide — starting with the
mandatory language policy (everything in English) and the engineering rules.
