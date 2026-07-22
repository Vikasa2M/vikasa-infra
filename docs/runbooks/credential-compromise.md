# Credential Compromise & Disaster-Recovery Playbook

**Break-glass runbook for the NATS trust chain and mTLS PKI.** When a key leaks, a
bundle is lost, or a rotation breaks the data plane, this is the "which key, what blast
radius, what do I type" reference — so the response isn't reconstructed under pressure.

> **Status / assumptions.** This playbook describes the bundle produced by
> `cmd/issue`, including the **operator and per-account signing-key separation**
> (`operator-sk.nkey`, `accounts/<NAME>-sk.nkey`). Steps marked **[automated]** use tooling that exists today;
> steps marked **[MANUAL]** are procedures with no command yet — see
> [Tooling gaps](#tooling-gaps) for what's queued for automation. Substitute your real
> `-spec`, `-out`, and `-cabinets` paths for the placeholders below.

---

## 0. First principles (read once, internalize)

- **`.nkey` and `.key` files are the only secrets.** Everything else in the bundle —
  `*.jwt`, `*.crt`, `resolver.conf`, `accounts.index`, `revocations/retired.jsonl` — is
  **public** material (public keys + signed claims). A leaked **JWT is not an incident**;
  the operator JWT is deliberately handed to every server. A leaked **seed/private key
  is** the incident. Always confirm *which artifact* leaked before reaching for the
  nuclear option.
- **The bundle is rebuildable from the spec.** `cmd/issue` is idempotent: every run
  re-signs all account JWTs, re-emits every cabinet `.creds`, and re-signs every client
  cert from the current model. **Keypairs are mint-once** — they are only regenerated if
  you (a) delete the seed file, or (b) name the cabinet in `-rotate`. This is the lever
  behind most recovery steps: *delete the compromised seed, re-run, redistribute.*
- **Revocation is declarative and automatic.** Every `cmd/issue` run folds the public
  ids in `revocations/retired.jsonl` into the issuing account JWT's `Revocations`. Old
  user JWTs are rejected by the resolver as soon as the re-signed account JWT is pushed.
- **The management plane is the break-glass.** Cabinets are reachable out-of-band over a
  **management IP / SSH**, independent of the NATS data-plane credential. If a rotation
  or re-anchor breaks a cabinet's NATS connection, SSH in over the management IP to push
  the new creds or roll back. **Invariant: management access must never ride on the NATS
  identity being rotated** — keep its auth and network separate, or a bad rotation locks
  you out of your own recovery path.

## 1. Bundle map

| Path | Secret? | Role |
|------|:-------:|------|
| `operator.nkey` | **SECRET** | operator **root** seed — anchor of trust; should be offline/cold |
| `operator.jwt` | public | operator JWT (self-signed by root, declares signing keys); baked into every server config |
| `operator-sk.nkey` | **SECRET** | operator **signing-key** seed — signs account JWTs day-to-day |
| `accounts/<NAME>.nkey` | **SECRET** | account **root** seed — account identity (JWT Subject); signs nothing after SK separation, can go offline |
| `accounts/<NAME>-sk.nkey` | **SECRET** | account **signing-key** seed — signs that account's user JWTs day-to-day |
| `resolver/<PUB>.jwt` | public | account JWT, keyed by account pubkey; served by the resolver |
| `cabinets/<district>/<id>.nkey` | **SECRET** | cabinet user seed |
| `cabinets/<district>/<id>.creds` | **SECRET** | cabinet user JWT + seed (NATS creds file) |
| `cabinets/<district>/<id>.key` | **SECRET** | cabinet client-cert private key (mTLS) |
| `cabinets/<district>/<id>.crt` | public | cabinet client cert (signed by the cabinet CA) |
| `ca/cabinet-ca.key` | **SECRET** | mTLS client CA private key |
| `ca/cabinet-ca.crt` | public | mTLS client CA cert (trust bundle for servers) |
| `accounts.index`, `resolver.conf`, `revocations/retired.jsonl` | public | index / server config / revocation log |

## 2. Triage — blast radius at a glance

| Compromised secret | Blast radius | Re-anchor servers? | Tooling | Section |
|--------------------|--------------|:------------------:|---------|:-------:|
| Operator **root** seed | **Entire trust domain** | **Yes — new operator** | **[MANUAL]** | [A](#a-operator-root-seed-compromise) |
| Operator **signing-key** seed | All accounts (re-sign) | No (same anchor) | **[automated]** swap | [B](#b-operator-signing-key-seed-compromise) |
| Account **signing-key** seed | One account (re-sign users) | No (same anchor) | **[automated]** swap | [C1](#c1-account-signing-key-seed) |
| Account **root** seed | One account + its cabinets (re-key, new pubkey) | No | semi-[automated] | [C2](#c2-account-root-seed) |
| Cabinet user/cert seed | One cabinet | No | **[automated]** | [D](#d-cabinet-credential-compromise) |
| Cabinet **CA** key | All cabinet client certs | No (re-trust CA bundle) | semi-[automated] | [E](#e-cabinet-x509-ca-key-compromise) |
| Whole bundle lost | DR | depends | rebuild | [F](#f-total-bundle-loss--disaster-recovery) |

---

## A. Operator root seed compromise

**`operator.nkey` leaked.** Worst case: the root of trust is untrustworthy. An attacker
can mint a signing key, accounts, users — anything. There is **no shortcut**; the
operator's identity itself must change.

**Detect.** Unexpected signing keys/accounts appearing; access from the offline-key
host; integrity alarm on the cold-storage medium.

**Contain.** Treat the entire trust domain as hostile. If feasible, isolate the NATS
servers from untrusted networks until re-anchored. Do **not** mint anything with the old
root.

**Recover — [MANUAL], full re-anchor:**
1. On an air-gapped host, mint a **new operator** (delete `operator.nkey` +
   `operator-sk.nkey` and re-run `cmd/issue` against a fresh `-out` dir — mint-once will
   create a new root + signing key).
2. Re-sign the whole tree from the spec: `cmd/issue -spec <spec> -out <newdir>
   -cabinets <inv>` re-mints accounts (new pubkeys), re-signs all account/user JWTs, and
   re-issues `.creds`. **[automated]** within the new bundle.
3. **Reconfigure every NATS server** to trust the new `operator.jwt` (it's in server
   config, *not* resolver-distributed). This is the manual, every-node step.
4. Push the new account JWTs into each server's resolver, and distribute new `.creds`
   to every cabinet (over the **management plane** — section 0).
5. Decommission the old operator: destroy old seeds; the old anchor is dead once no
   server trusts it.

**Verify.** `cmd/credhealth -dir <newdir>` is clean; a test client connects with a new
cabinet `.creds`; old `.creds` are rejected.

---

## B. Operator signing-key seed compromise

**`operator-sk.nkey` leaked.** The bounded case the signing-key separation was built
for. The attacker can sign rogue account JWTs, **but the operator identity is unchanged**
— servers keep trusting the same operator, so this is a key *swap*, not a re-anchor.

**Detect.** Account JWTs in the resolver whose issuer is the SK pubkey but that you
didn't issue; `cmd/credhealth` / account inventory mismatch vs. the spec.

**Contain.** Stop issuing with the compromised SK immediately.

**Recover — [automated]:**
1. `cmd/issue -rotate-operator-sk -spec <spec> -out <dir>` does it in one command:
   it records the old (compromised) SK pub to `revocations/retired-operator-sk.jsonl`,
   force-mints a new `operator-sk.nkey`, rebuilds the operator JWT so its `SigningKeys`
   lists **only the new key** (the old one is dropped immediately — an attacker's key
   must not stay trusted), and re-signs every account JWT with the new SK.
2. Reload the updated `operator.jwt` on every server (config reload of the *same*
   anchor — far cheaper than a re-anchor) and push the re-signed account JWTs to the
   resolver, over the **management plane**. A brief cutover gap is expected and accepted
   — untrusting the leaked key wins over zero-downtime here.

**Verify.** Decode an account JWT — issuer == new SK pubkey; operator JWT `SigningKeys`
no longer contains the leaked key. Any rogue account JWT signed by the old SK is now
rejected (its issuer is no longer a trusted signing key).

---

## C. Account-level seed compromise

Two distinct account-level secrets, two recovery paths. The **signing key**
(`accounts/<NAME>-sk.nkey`) signs the account's user JWTs day-to-day — rotating it is a
one-command **[automated]** swap (C1). The **root** seed (`accounts/<NAME>.nkey`) is the
account's identity (the JWT Subject) and signs nothing after the SK separation; re-keying
it changes the account pubkey and cascades (C2).

### C1. Account signing-key seed

**`accounts/<NAME>-sk.nkey` leaked** (e.g. `DISTRICT_D7-sk.nkey`). After the account
signing-key separation, **this** key — not the account root — signs every user JWT under
the account. A leak lets the attacker mint rogue users for that one account. The account
*identity* (root pubkey / Subject) is unchanged, so this is a key **swap** within the
account, not an account re-key: the operator and other accounts are untouched, and
cabinets keep the same account.

**Detect.** User JWTs under the account whose issuer is the account SK pubkey but that you
didn't issue; `cmd/credhealth` / inventory mismatch vs. the spec.

**Contain.** Stop issuing under the compromised account SK immediately.

**Recover — [automated]:**
1. `cmd/issue -rotate-account-sk=<NAME> -cabinets <inv> -spec <spec> -out <dir>` does it in
   one command: it records the old (compromised) SK pub to
   `revocations/retired-account-sk.jsonl`, force-mints a new `accounts/<NAME>-sk.nkey`,
   rebuilds the account JWT so its `SigningKeys` lists **only the new key** (the old one is
   dropped immediately — an attacker's key must not stay trusted), and **re-signs every
   user JWT under that account** with the new SK (fresh `.creds`). Comma-separate names
   (`-rotate-account-sk=DISTRICT_D7,DISTRICT_D8`) to rotate several accounts at once.
2. Reload the re-signed account JWT into the resolver and distribute the re-issued
   `.creds` to that account's cabinets, over the **management plane**. A brief cutover gap
   is expected and accepted — untrusting the leaked key wins over zero-downtime.

> **`-cabinets` is required to re-sign user creds.** Run with `-cabinets` so the district's
> user JWTs are re-signed under the new SK. Without `-cabinets`, the command still rotates
> the SK and drops the old one from the account JWT (the immediate "kill the compromised
> key" action), but the existing user `.creds` are **not** re-signed and will be rejected
> until you re-run with `-cabinets` — the tool prints a `WARNING` to that effect. Use the
> no-`-cabinets` form only for an emergency immediate kill when you will re-issue creds in
> a follow-up step.

**Note — no user revocation.** Unlike a cabinet rotation (section D), account-SK rotation
does **not** revoke user identities — the users keep their nkeys and pubkeys; only the key
that *signs* their JWTs changes. Old user JWTs stop validating because the account JWT no
longer lists the old SK, not via a `Revocations` entry. The retired SK pub in
`revocations/retired-account-sk.jsonl` is **audit-only** (forensics) — nothing consumes it.

**Out-of-band.** `cmd/issue` only produces the bundle; redistributing the re-signed
`.creds` and account JWT to the fleet is out-of-band (Ansible / golden-image / management
plane — sub-project E). An overlap/drain-window cutover is deliberately not built (same
rationale as operator-SK, section B / [Tooling gaps](#tooling-gaps)).

**Verify.** Decode a user JWT under the account — issuer == new account SK pubkey; the
account JWT `SigningKeys` no longer contains the leaked key; a cabinet connects with its
re-issued `.creds`; user JWTs signed by the old SK are rejected.

### C2. Account root seed

**`accounts/<NAME>.nkey` leaked** (e.g. `DISTRICT_D7`). The account root is the account's
identity (the JWT Subject) and signs nothing after the SK separation, so a leak is lower-
severity than a signing-key leak (C1) — but re-keying it mints a **new account pubkey**,
which cascades to every user (`IssuerAccount`) and the resolver JWT filename. Blast radius
is the account and its cabinets; the operator and other accounts are untouched.

**Recover — semi-[automated]:**
1. Delete `accounts/<NAME>.nkey` and the stale `resolver/<oldpub>.jwt`.
2. Re-run `cmd/issue -spec <spec> -out <dir> -cabinets <inv>`. **[automated]:** a new
   account keypair is minted (new pubkey), a new account JWT is signed by the operator
   SK, and **every user under it is re-signed** by the new account key with a fresh
   `.creds`.
3. Push the new account JWT to the resolver; distribute the re-issued `.creds` to that
   account's cabinets over the **management plane**.
4. The old account JWT (old pubkey) no longer exists in the resolver → old user JWTs
   under it stop validating.

**Verify.** `accounts.index` shows the new pubkey; a cabinet under the account connects
with its new `.creds`; old `.creds` are rejected.

---

## D. Cabinet credential compromise

**A cabinet's `.nkey`/`.creds`/`.key` leaked.** Smallest blast radius — one cabinet — and
**fully automated**. This is the routine rotation path.

**Recover — [automated]:**
1. `cmd/issue -rotate <id1,id2,...> -spec <spec> -out <dir> -cabinets <inv>` re-keys the
   named cabinets: new user nkey/JWT/`.creds` **and** new client-cert key/`.crt`. Their
   old public ids are captured to `revocations/retired.jsonl`.
2. The same run folds those retired pubkeys into the issuing account JWT's
   `Revocations` — the old user JWT is **revoked** (rejected by the resolver), not merely
   superseded.
3. Push the new `.creds` + `.crt` to the cabinet over the **management plane**; push the
   re-signed account JWT to the resolver.

**Bulk / proactive variant:** `cmd/issue -rotate-expiring <window> -cabinets <inv>`
(dry-run) then `--apply` rotates everything expiring within the window. Use
`cmd/credhealth -dir <dir> -warn <window>` to see what's near expiry first.

**Verify.** `cmd/credhealth` shows the cabinet's new 90-day validity; old JWT rejected;
new mTLS handshake succeeds.

---

## E. Cabinet X.509 CA key compromise

**`ca/cabinet-ca.key` leaked.** The attacker can forge client certs that chain to your
CA. Transport-layer only — NATS identity/authz is the JWT layer (section D), so a forged
cert alone still needs a valid `.creds` to do anything. Still: rotate the CA.

**Recover — semi-[automated]:**
1. Delete `ca/cabinet-ca.crt` **and** `ca/cabinet-ca.key`.
2. Re-run `cmd/issue -spec <spec> -out <dir> -cabinets <inv>`. **[automated]:** a new CA
   is minted (mint-once) and **every cabinet client cert is re-signed against it**
   (client *keys* are mint-once and reused — only the certs change).
3. **Update the server trust bundle** to the new `ca/cabinet-ca.crt` on every NATS
   server (this is the manual, every-node step — the CA cert is the mTLS trust root).
4. Distribute the re-signed `.crt`s to cabinets over the **management plane**.

> **Note:** there is **intentionally** no CRL — not a gap, a decision. NATS core does not
> CRL-check client certs, and the real authz is the JWT layer (a cert alone is useless
> without valid `.creds`), so a CRL would have no enforcement consumer in this
> architecture. The CA swap + re-trust *is* the revocation for transport; client certs also
> self-expire at 90 days; forged certs die when servers stop trusting the old CA cert. A
> CRL would only earn its keep if an external mTLS terminator (sub-project E) is introduced
> — the retired cert serials in `revocations/retired.jsonl` are already the data source if
> that day comes.

**Verify.** New CA serial in `ca/cabinet-ca.crt`; a cabinet completes an mTLS handshake
with its re-signed cert against the new CA; certs against the old CA are rejected.

---

## F. Total bundle loss / disaster recovery

**The creds bundle is gone** (lost laptop, wiped disk) with no backup of the seeds.

- **If the operator + account seeds are unrecoverable:** this is equivalent to an
  operator re-anchor — follow **[Section A](#a-operator-root-seed-compromise)** to mint a
  fresh trust domain from the spec and re-anchor every server. The **spec is the source
  of truth**; the topology and account model regenerate identically, only the keypairs
  are new.
- **If you have a backup of the seeds:** restore the bundle dir, run `cmd/issue` once to
  re-sign/verify, and you're back — no re-anchor needed (pubkeys unchanged).
- **Cabinets stay reachable** throughout via the management plane, independent of the
  NATS data plane — so even a full re-anchor can push fresh `.creds` without physical
  access.

**Backup guidance (preventive):** the seeds (`*.nkey`, `*.key`, `ca/cabinet-ca.key`) are
the only irreplaceable material. Back them up encrypted and offline. Everything else
regenerates from the spec. **The operator root seed especially belongs in cold,
offline storage** — that is the entire point of the signing-key separation.

---

## Tooling gaps

What this playbook needs that **does not exist yet** (do it manually until built):

- **Operator-SK rotation with a zero-gap drain window** — `cmd/issue -rotate-operator-sk`
  is built, but it does an **immediate swap** (force-mint new SK, drop the old one now,
  re-sign accounts, retire the old pub to `revocations/retired-operator-sk.jsonl`). A
  zero-gap **overlap/drain-window** cutover (keep the old SK trusted while the fleet
  reloads) is deliberately **not** built here — it would leave a *compromised* key trusted
  during the drain, and zero-gap fleet cutover belongs to sub-project E's distribution
  machinery, not the offline issuer.
- **Operator root re-anchor helper** — section A is fully manual by design (rare, high-
  stakes, every-server config change). No automation planned.
- **Account signing-key drain-window cutover** — `cmd/issue -rotate-account-sk=<NAME>`
  (section C1) is built and **automated** (immediate swap: force-mint new SK, drop the old
  one, re-sign the account's user JWTs, retire the old pub to
  `revocations/retired-account-sk.jsonl`). What's deliberately **not** built is a zero-gap
  overlap/drain-window cutover — same rationale as operator-SK above (it would leave a
  compromised key trusted during the drain; fleet cutover belongs to sub-project E).
- **Account root re-key flag** — re-keying an account **root** (section C2, new pubkey)
  still works by deleting the seed + re-running; there's no dedicated `-rotate-account-root`
  flag.
- **Server-side push** — distributing operator JWT / CA bundle / resolver JWTs to servers
  and `.creds` to cabinets is out-of-band (Ansible / golden-image / management plane);
  `cmd/issue` only produces the bundle.

## Post-incident checklist

- [ ] Confirmed which **secret** (not JWT) actually leaked.
- [ ] Compromised seed destroyed; replacement minted.
- [ ] Old identity **revoked** (JWT `Revocations`) or **untrusted** (operator
      `SigningKeys` / CA bundle), not merely superseded.
- [ ] Servers reloaded/re-anchored as required; resolver updated.
- [ ] New creds distributed to affected cabinets over the management plane.
- [ ] `cmd/credhealth -dir <dir>` clean; a live client connects with new creds; old
      creds rejected.
- [ ] Seeds re-backed-up (encrypted, offline); root seed returned to cold storage.
- [ ] Timeline + root cause recorded.
