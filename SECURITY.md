# Security Policy

## Reporting a vulnerability

Please do **not** open a public issue for security problems. Instead, report
vulnerabilities privately via
[GitHub Security Advisories](https://github.com/Vikasa2M/vikasa-infra/security/advisories/new)
("Report a vulnerability" on the repository's Security tab).

You should receive an acknowledgement within a few days. Please include enough
detail to reproduce the issue (affected command or package, a minimal topology
spec if relevant, and observed vs expected behavior).

## Scope notes

This repository generates deployment artifacts and credentials; it does not run
services. Reports of particular interest:

- Credential issuance (`cmd/issue`, `internal/issuance`, `internal/pki`):
  anything that weakens the trust chain, widens a cabinet's subject-space
  permissions beyond its district, or leaks seeds/keys (file modes, lifetime,
  wiping).
- DMZ isolation (`internal/plan`, `internal/render`): generated configurations
  that would let an external consumer publish into, or subscribe beyond, its
  granted share.
- Any generated NATS/Kubernetes artifact that is more permissive than the
  topology spec declares.

## Supported versions

Only the latest release (and `main`) receives security fixes.
