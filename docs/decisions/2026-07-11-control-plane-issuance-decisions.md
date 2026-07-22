# ADR: Control-plane issuance — DMZ external-consumer credentials + scale-item triage

Status: **Accepted** — 2026-07-11
Scope: `cmd/issue` / `internal/issuance` (the operator/JWT control plane)
Linear: MON-53 (this decision), MON-50 & MON-52 (parked, reasoning below)

This is the first decision record in `docs/decisions/`. Format: Context → Decision →
Alternatives considered (with why-rejected) → Consequences. One file per decision
cluster; keep the rationale, not just the outcome.

---

## Context

- **Operator/JWT mode is the production auth model** — per `docs/ARCHITECTURE.md` §8,
  NKEY/JWT accounts are the identity + authorization system of record. The
  config-mode `accounts.conf` that `cmd/gen` renders is explicitly the
  *dev/reviewable/non-operator form* (see `internal/render/accounts.tmpl` header).
- `accounts.Build` already emits `UserTemplate`s for **DMZ external consumers**
  (subscribe-only, `PublishDeny: [">"]`) and **SYSTEM**, but `internal/issuance`
  never reads `.Users` — it mints credentials only for **cabinets** (from
  `fleet.Inventory`).
- **The gap:** in operator mode, external DMZ partners have no minted, scoped,
  revocable credential. So the "DMZ validated end-to-end" claim (§7) holds only at
  the render/config layer (`test/integration/dmz_flow_test.go`, `accounts.tmpl`),
  **not** at the operator-mode trust-chain layer that `issuance` owns.
- Near-term roadmap includes operator mode **and** real external DMZ partners, so
  this gap is blocking, not hypothetical. (Confirmed with the product owner
  2026-07-11 — this is why MON-53 was promoted from scope-debt to active work.)

## Decision

Add DMZ external-consumer credential issuance to `cmd/issue` (MON-53), scoped as:

1. **DMZ consumers only, mint-only.** SYSTEM and consumer-cred rotation/revocation
   are clean follow-ons, not in this cycle.
2. **JWT-only credentials** — a `.creds` (user JWT + seed) per consumer; **no**
   per-partner mTLS client cert.
3. **One credential per consumer**, aggregating the subjects of all that consumer's
   shares (union, deduped, sorted). The aggregation lives in `issuance`, so
   `accounts.Build` / `accounts.conf` and their golden trees are untouched.
4. **Driven by the DMZ account's `UserTemplate`s** in `accounts.Model`; signed by
   the **DMZ account signing key** (already minted by `ensureAccounts`); it
   **honors the deny lists** — this is the first operator-mode enforcement of the
   DMZ deny-by-default invariant (`Sub.Allow = [share subjects]`, `Pub.Deny = [">"]`).

The mint itself reuses existing machinery (`ensureKeypair`, `jwt.FormatUserConfig`,
the Task-2 seed/creds wipe, the `requireOwnerOnly` sweep) — it is a *second consumer
of the mint path*, not new crypto.

## Alternatives considered

- **JWT + mTLS client cert (full cabinet parity).** *Rejected for now.* An external
  partner is a **client**, not an interior leaf link; §7 deliberately frames external
  consumers as per-consumer JWTs. The JWT already provides identity + authz +
  revocation, and server-side TLS on the DMZ endpoint encrypts the wire. A
  per-partner client cert adds a certificate issuance/rotation relationship with each
  outside party for marginal gain. mTLS stays available as an opt-in add-on for a
  specific partner that ever requires it.
- **DMZ + SYSTEM, full lifecycle (mint + rotate/revoke).** *Rejected.* DMZ consumers
  are what actually unblocks external partners; keeping it DMZ-only and mint-only
  keeps the change small and reviewable. SYSTEM (a `$SYS`/monitoring principal) and
  consumer-cred rotation are independent follow-ons.
- **One credential per share (instead of per consumer).** *Rejected.* "Per-consumer
  JWT" is the model in §7; a consumer subscribing to multiple corridors gets one
  credential scoped to all of them. Per-share would also collide on the
  `<consumer>.creds` filename.
- **Reshape `accounts.Build` to dedupe consumers into one `UserTemplate`.**
  *Rejected.* Cleaner in the abstract but changes `accounts.conf` golden output;
  aggregating in `issuance` at mint time is the lower-blast-radius choice.

## Consequences

- New `mintDMZConsumers` step (sibling to `mintCabinets`); new
  `dir/dmz/<consumer>.{creds,nkey}` output at `0600`; `dir/dmz` joins the fail-closed
  `requireOwnerOnly` sweep; `issuance.Result` gains a deterministic (sorted)
  DMZ-consumer list, surfaced in the `cmd/issue` summary.
- **No `cmd/gen` golden impact** — this is `cmd/issue`-only.
- Follow-ons (tracked separately): consumer-cred rotation/revocation, SYSTEM
  issuance, optional per-partner mTLS.

---

## Related decisions (same session): scale-item triage

These were originally filed as deferred "10k-scale" work. Analysis during this
session showed both were lower-value than their framing suggested; recording the
reasoning so they are not reflexively rebuilt.

### MON-50 — parallel per-cabinet minting — **PARKED (low value)**

Minting 10k cabinets is a **one-time, offline, idempotent, incrementally-onboarded**
operation: ~15–90s single-core, and steady-state runs mint only *new* cabinets
(issuance is idempotent; the architecture onboards cabinets in batches over months).
The only cold-10k case is disaster re-keying — a rare, offline, 2am event where 90s
is a non-event. Parallelizing optimizes a batch job nobody waits on; not worth making
the `pki.Signer` concurrency-safe now. Kept as a someday-nice-to-have.

### MON-52 — revocation-log compaction — **REFRAMED / PARKED**

The premise ("account-JWT `Revocations` grows unbounded at fleet scale") is mostly
false given the design:

- The `Revocations` map is fed by **exactly one path**: cabinet **re-keys**
  (`captureRetired`, triggered by `-rotate` / `-rotate-expiring`). The two
  SK-rotation logs (`retired-operator-sk.jsonl`, `retired-account-sk.jsonl`) are
  **audit-only** — nothing consumes them.
- Per §8, **re-key is reserved for compromise**; routine renewal is **assertion-only**
  (re-run `cmd/issue`, keypair stays, no revocation). So the revocation list grows
  with **compromise events (rare)**, not the ~90-day fleet renewal cycle. Unbounded
  growth does not occur if operations follow the design → compaction is moot.
- Cost-model corrections: revocations are checked per-connect as a **cheap map lookup**
  in the already-decoded account JWT (not re-parsed on every connect), and they live in
  `cmd/issue`'s **resolver bundle** (operator mode, deployed out-of-band) — **not** in
  the helm/cabinet config `cmd/gen` produces.

**Real (small) residual item:** `-rotate-expiring` performs a **re-key** (new keypair +
revocation) but its name reads like a routine "renew expiring creds" button. Used that
way it floods the revocation list. The correct fix is small — make `-rotate-expiring`
assertion-only (reuse the keypair, no revocation), or rename/document it so
renew-vs-rekey is unmistakable — **not** compaction.
