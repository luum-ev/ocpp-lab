# Security Policy

ocpp-lab is a **testing tool**: it should only ever be pointed at CSMS
endpoints you own or are authorized to test. Do not point fleets at
third-party production systems.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on this repository
(Security → Report a vulnerability). We will respond within 7 days.

## Scope notes

* v1 speaks `ws://`/`wss://`. Security Profile 2 (HTTP Basic Auth with a
  station's `authorizationKey`) is supported; Security Profile 3 (TLS client
  certificates) is not. Use `wss://` whenever a key is set: over `ws://` the
  key travels in the clear.
* The control API (`:8887`) has no authentication by design. Treat it as
  trusted-network only and never expose it publicly; when running the
  container, publish it on the loopback interface (`-p 127.0.0.1:8887:8887`).
* In the Helm chart, the fleet (including any `authorizationKey`) is stored
  in plain text in a ConfigMap. Use test keys only.
