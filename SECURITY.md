# Security Policy

ocpp-lab is a **testing tool**: it should only ever be pointed at CSMS
endpoints you own or are authorized to test. Do not point fleets at
third-party production systems.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on this repository
(Security → Report a vulnerability). We will respond within 7 days.

## Scope notes

* v1 speaks `ws://`/`wss://` with no client certificates (OCPP security
  profiles 2/3 are future work) — treat the control API (`:8887`) as trusted-
  network only; it has no authentication by design and must not be exposed
  publicly.
