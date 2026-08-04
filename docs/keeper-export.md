# Keeper Export Operations Runbook

This runbook covers the supported `keeper-export/v1` deployment from a private CLIProxyAPIPlus (CPA) instance to a Keeper service. It is the operator source of truth for migration, rollback, monitoring, and release coordination. Keeper database procedures are documented in the Keeper repository's [migration and recovery runbook](../../cpa-usage-keeper/docs/keeper-export.md), and Management Center usage is documented in the UI repository's [operator guide](../../Cli-Proxy-API-Management-Center/docs/keeper-export.md).

## Supported topology and trust boundaries

```text
operator browser
    |
    | existing CPA management authentication
    v
Management Center -> private CPA management API
                         |
                         | outbound HTTPS only
                         | Bearer instance credential
                         v
                  public/private Keeper /api/v1/export/*
                         |
                         v
                    Keeper SQLite
```

- CPA is the trusted source of usage and metadata. Each CPA has one durable outbox and one Keeper instance credential.
- Keeper is the trusted receiver and assigns the instance identity from the authenticated credential. CPA never sends an authoritative `instanceId` in a request body or query.
- The browser talks only to CPA's existing management API. It never receives the Keeper token, client private key, or resolved environment value.
- The supported protocol is exactly `keeper-export/v1`. Do not place a JSON-rewriting, decompressing, or body-mutating proxy between CPA and Keeper.
- Keeper accepts `application/json` without content encoding. CPA disables automatic compression and rejects redirects, including HTTPS-to-HTTPS redirects.
- Keeper ingress must use HTTPS, including local QA. A private CA and optional mTLS are supported; there is no insecure HTTP or skip-verify mode.
- A Keeper credential is immutably bound to one CPA instance. Use a different instance and credential for every CPA, even if several CPAs have the same display name.
- API-key fingerprints are HMACs bound to the Keeper instance identity. The same raw key used by two CPAs produces different fingerprints, so cross-instance correlation is intentionally unavailable.

## Deployment order

Use this order. Do not enable a CPA exporter before Keeper migration and credential bootstrap are complete.

1. Stop Keeper and make a pre-migration SQLite backup.
2. Start the new Keeper binary once and let its forward-only migrations complete.
3. Create one Keeper instance per CPA and capture each one-time credential.
4. Store each credential in that CPA's private environment and configure HTTPS trust, custom CA, and optional mTLS.
5. Configure the durable outbox mount and save exporter settings while still disabled.
6. Run the non-mutating connection test (`identity:test`).
7. For the canary, stop/drain request traffic, finish and disable legacy pull, enable push, wait for first identity binding/healthy status, then resume traffic and verify ACK/backlog/revisions.
8. Repeat that quiet cutover for the remaining CPA exporters one at a time.
9. Deploy the matching Management Center release after the CPA management API is available.

Keeper instance creation and database migration commands are in the [Keeper migration and recovery runbook](../../cpa-usage-keeper/docs/keeper-export.md).

## Exact CPA configuration

### Secret environment

The YAML stores only an environment-variable name. Put the one-time token in the process/container environment, never in YAML:

```env
CPA_KEEPER_INGEST_TOKEN=<paste-the-one-time-token-here>
```

For systemd, prefer a root-owned environment file outside the repository:

```bash
sudo install -d -m 0700 -o root -g root /etc/cli-proxy-api
sudo install -m 0600 -o root -g root /dev/null /etc/cli-proxy-api/keeper-export.env
sudoedit /etc/cli-proxy-api/keeper-export.env
```

Configure the service with `EnvironmentFile=/etc/cli-proxy-api/keeper-export.env`. Do not pass the token on a command line: command arguments may be visible to other local users.

For Compose, reference an untracked env file and mount a persistent outbox directory:

```yaml
services:
  cli-proxy-api:
    env_file:
      - ./secrets/keeper-export.env
    volumes:
      - ./data/keeper-export:/var/lib/cli-proxy-api/keeper-export
      - ./secrets/keeper-ca.pem:/run/secrets/keeper-ca.pem:ro
      - ./secrets/cpa-client.pem:/run/secrets/cpa-client.pem:ro
      - ./secrets/cpa-client-key.pem:/run/secrets/cpa-client-key.pem:ro
```

Protect host files before starting the container:

```bash
install -d -m 0700 ./data/keeper-export ./secrets
chmod 0600 ./secrets/keeper-export.env ./secrets/keeper-ca.pem \
  ./secrets/cpa-client.pem ./secrets/cpa-client-key.pem
```

### YAML, initially disabled

Enable CPA's existing usage event source and add this complete block to `config.yaml`. Paths are absolute on the CPA host/container. The example contains no token value.

```yaml
usage-statistics-enabled: true

usage-export:
  enabled: false
  mode: disabled
  keeper:
    url: "https://keeper.example.com"
    token-env: "CPA_KEEPER_INGEST_TOKEN"
    ca-file: "/run/secrets/keeper-ca.pem"
    client-cert-file: "/run/secrets/cpa-client.pem"
    client-key-file: "/run/secrets/cpa-client-key.pem"
  outbox:
    path: "/var/lib/cli-proxy-api/keeper-export/outbox.db"
    max-bytes: 1073741824
  delivery:
    max-batch-events: 500
    max-batch-bytes: 1048576
    flush-interval-ms: 1000
    request-timeout-ms: 15000
    initial-backoff-ms: 1000
    max-backoff-ms: 60000
  metadata:
    enabled: true
    interval-ms: 300000
    categories:
      - auth_files
      - api_keys
      - provider_identities
  privacy:
    include-client-ip: false
    include-forwarded-for: false
    include-user-agent: false
```

Rules:

- `usage-statistics-enabled` must be `true` before `enabled:true`/`mode:push` is valid.
- `keeper.url` is an absolute HTTPS base URL with host and optional base path, no query/fragment/userinfo. CPA appends `/api/v1/export/...`; set the base path only when Keeper is served under the matching `APP_BASE_PATH`.
- `token-env` must match `[A-Z_][A-Z0-9_]{0,127}`. A raw `token` field is unsupported and rejected.
- `ca-file` is optional for a publicly trusted certificate. `client-cert-file` and `client-key-file` must be set together for mTLS.
- Keep all privacy switches `false` unless collection of those client identifiers is explicitly approved.
- The outbox path must be durable and local to one CPA. Never share one outbox file between processes or CPAs.
- The current default outbox quota is 1 GiB; the default batch body limit is 1 MiB. Size the outbox for the longest expected Keeper outage and alert before it fills.

### Safe preflight and enablement

Validate the binary/flags without starting a service:

```bash
go run ./cmd/server -version >/dev/null
```

For the full config, save the disabled draft through the authenticated Management Center/API path; that path validates and hot-reconciles the real configuration. For a manual startup drill, copy the config into a temporary directory, replace every writable/auth/log/outbox path and listen port with temporary values, and run only that copied environment. Never point a QA process at the production auth directory, outbox, or config. In Management Center, use **Test Connection** while the exporter is disabled. A successful test proves HTTPS, CA/mTLS, token resolution, scope, and credential-bound identity without binding or mutating the outbox.

**Cutover warning:** the outbox binds to the Keeper instance asynchronously after enablement. Usage arriving before that first identity/binding succeeds cannot be durably appended and is surfaced as a blocked/degraded error. Stop or drain request traffic, disable the legacy pull collector, enable push, wait for the expected instance and a healthy status, then resume traffic.

After the test identifies the expected Keeper instance, switch only these values and save:

```yaml
usage-export:
  enabled: true
  mode: push
```

Saving through Management Center or the management API hot-applies the exporter. A CPA restart is not required. Keeper failures never fail the originating model request; local append/delivery failures appear in exporter status.

## Durable outbox operations

### Location, ownership, quota, and backup

- Persist `outbox.db` and its SQLite sidecars (`-wal`, `-shm`, `-journal`) on durable storage. CPA enforces owner-only `0600` permissions on these files.
- Make the parent directory owner-only (`0700`) and owned by the CPA service account.
- Monitor both `backlogBytes` and filesystem free space. `max-bytes` is an admission quota; once full, new projected usage cannot be queued and status becomes degraded.
- Never inspect or edit the live SQLite file with ad hoc SQL. It contains secret-free projected usage, but it is operationally sensitive and its stream/sequence metadata must remain consistent.

A consistent backup requires the exporter to be disabled or the CPA service to be stopped. Disabling closes the outbox but preserves every queued event:

```bash
# 1. Save enabled:false and mode:disabled, then verify status.state == disabled.
# 2. Back up the closed file and any sidecars.
backup_dir="/secure-backups/cpa-outbox/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$backup_dir"
shopt -s nullglob
outbox_files=(/var/lib/cli-proxy-api/keeper-export/outbox.db*)
if ((${#outbox_files[@]} == 0)); then
  printf '%s\n' 'No outbox files found; verify usage-export.outbox.path.' >&2
  exit 1
fi
cp --preserve=mode,timestamps "${outbox_files[@]}" "$backup_dir/"
sha256sum "$backup_dir"/outbox.db* >"$backup_dir/SHA256SUMS"
chmod 0600 "$backup_dir"/*
```

Store backups encrypted and with the same access restrictions as the live data.

### Recovery

To recover the same CPA instance and stream, stop/disable the exporter, restore the complete backup set to the same path, enforce ownership and modes, restore the same Keeper credential, then enable. Keeper replay handling is idempotent by `(credential-bound instance, streamId, sequence, payload digest)`.

```bash
# WARNING: the following replacement is destructive to the current outbox.
sudo systemctl stop cli-proxy-api
restore_dir=/secure-backups/cpa-outbox/20260804T120000Z
shopt -s nullglob
restore_files=("$restore_dir"/outbox.db*)
if ((${#restore_files[@]} == 0)); then
  printf '%s\n' 'No restore files found; aborting before replacement.' >&2
  exit 1
fi
SAFETY="/secure-backups/cpa-outbox/pre-restore-$(date -u +%Y%m%dT%H%M%SZ)"
sudo install -d -m 0700 "$SAFETY"
current_files=(/var/lib/cli-proxy-api/keeper-export/outbox.db*)
if ((${#current_files[@]} > 0)); then
  sudo cp -a "${current_files[@]}" "$SAFETY/"
fi
sudo install -d -m 0700 -o cli-proxy -g cli-proxy /var/lib/cli-proxy-api/keeper-export
sudo rm -f \
  /var/lib/cli-proxy-api/keeper-export/outbox.db \
  /var/lib/cli-proxy-api/keeper-export/outbox.db-wal \
  /var/lib/cli-proxy-api/keeper-export/outbox.db-shm \
  /var/lib/cli-proxy-api/keeper-export/outbox.db-journal
sudo cp --preserve=mode,timestamps "${restore_files[@]}" \
  /var/lib/cli-proxy-api/keeper-export/
sudo chown cli-proxy:cli-proxy /var/lib/cli-proxy-api/keeper-export/outbox.db*
sudo chmod 0600 /var/lib/cli-proxy-api/keeper-export/outbox.db*
sudo systemctl start cli-proxy-api
```

Do not delete/reset the outbox to resolve an ACK gap or credential problem. Deleting it creates a new stream and permanently abandons queued data. A new stream is appropriate only when the operator has explicitly accepted that loss or the old outbox has been safely archived and all queued data is known delivered.

## Delivery and failure semantics

- Usage is appended to SQLite before network delivery. The worker sends ordered batches and deletes only events acknowledged contiguously by Keeper.
- Exact replay after timeout or response loss is accepted without double counting. A response can be lost after Keeper commits; retrying the same sequence/digest is safe.
- Keeper restart or network outage leaves events queued. CPA retries with bounded exponential backoff and honors retryable protocol errors.
- Out-of-order arrival is stored, but the ACK watermark advances only across a contiguous sequence. `nextExpectedSequence` identifies the first gap.
- Reusing an existing sequence with a different payload produces `conflicting_replay`; do not reset either database. Preserve both sides and investigate stream corruption or accidental outbox reuse.
- The outbox is permanently bound to the Keeper instance returned by `/api/v1/export/identity`. A token for another instance produces an instance-binding mismatch and must not be forced.
- Metadata is independent of usage ACKs. Each category uses monotonically increasing revisions and complete snapshots.

## Metadata complete-snapshot semantics

The fixed categories are `auth_files`, `api_keys`, and `provider_identities`.

- Every request is `complete:true`: its item set is the full current category for one credential-bound instance.
- Only a newer, fully validated complete snapshot may replace the category and mark missing rows stale.
- An empty newer complete snapshot intentionally clears that category for that instance.
- Exact replay of the same revision and digest is idempotent. Same revision with different content is a conflict. An older revision is stale and must not delete anything.
- A failed, incomplete, malformed, or unauthorized snapshot leaves the previously applied snapshot intact.
- Revisions are per CPA outbox/category and appear in Management Center status. Never copy revision state between unrelated CPAs.

## Monitoring and alerts

All four operations use CPA management authentication; Keeper bearer credentials are not accepted on these routes:

- `GET /v0/management/usage-export/settings`
- `PUT /v0/management/usage-export/settings`
- `POST /v0/management/usage-export/test`
- `GET /v0/management/usage-export/status`

Settings responses are redacted, the test operation uses draft values without saving them, and status exposes only sanitized runtime state.

Read `GET /v0/management/usage-export/status` through authenticated management access or use the Management Center Keeper Export section.

| State | Meaning | Operator action |
|---|---|---|
| `disabled` | Exporter intentionally off; no worker or open outbox | Expected during rollout/rollback. Confirm queued file is retained. |
| `starting` | Enabled but no successful authenticated delivery yet | Check token environment, HTTPS trust, mTLS, and Keeper availability. |
| `connected` | Last cycle succeeded; backlog may still be draining | Watch ACK and backlog trend until stable. |
| `retrying` | Retryable error with a scheduled retry | Alert on duration and backlog growth; inspect sanitized error code. |
| `degraded` | Non-retryable/config/storage condition | Stop rollout; fix credential, binding, permissions, quota, or protocol mismatch. |
| `blocked` | Local outbox cannot safely continue, such as full/exhausted/binding failure | Disable exporter, preserve files, and repair before accepting more traffic. |

Monitor these fields together:

- `backlogEvents`, `backlogBytes`, `oldestBacklogAt`: alert on sustained growth, age beyond the recovery objective, and 70%/85% of `max-bytes`.
- `nextSequence`, `acknowledgedThrough`, `nextExpectedSequence`: ACK should advance; a persistent mismatch indicates a gap or remote state issue.
- `lastAttemptAt`, `lastSuccessAt`, `nextRetryAt`: distinguish idle/healthy from a retry loop.
- `metadataRevisions`: each enabled category should eventually advance after source changes.
- `lastError.code`, `.message`, `.retryable`: messages are sanitized; use the code before increasing log verbosity.
- `instance.instanceId` and display name: verify the CPA is bound to the intended Keeper instance.

## Credential lifecycle

Keeper credentials contain immutable scopes. A bootstrap/export credential normally needs all three:

```json
["usage:push", "metadata:push", "identity:test"]
```

- **Issue:** create a new credential for the same Keeper instance. Capture the returned token once and place it in the CPA environment.
- **Rotate:** Keeper creates the replacement and revokes the old credential atomically. Update the CPA environment immediately, restart/recreate the CPA process so it sees the new value, then run Test Connection and verify status. If the process cannot be updated in the same window, issue a second credential first, cut over, verify, then revoke the old credential.
- **Revoke:** immediately invalidates that credential. Queued data remains in CPA, but delivery retries fail until a valid same-instance credential is installed.
- **Disable instance:** blocks all credentials for that instance. Use it for containment, not ordinary maintenance. Re-enable the same instance to resume the same outbox stream.
- **Bootstrap:** `identity:test` is required before an unbound outbox can learn and persist its Keeper instance. A usage-only credential cannot perform first binding or the Management Center connection test.

Never use a credential from another CPA instance to bypass a failure. The outbox binding correctly rejects it.

## Safe disable and rollback

### Disable without data loss

1. Save `enabled:false` and `mode:disabled`.
2. Confirm status is `disabled`.
3. Leave the outbox volume/file in place.
4. Keep the Keeper instance and credential unless performing a security revocation.
5. Re-enable later with the same outbox and same Keeper instance; queued events resume.

### Roll back CPA or UI

- Roll back Management Center independently by serving the previous `management.html`; this does not alter CPA settings or outbox data.
- To roll back CPA to a binary that does not understand `usage-export`, first disable the exporter with the new binary and back up the closed outbox. Preserve the YAML block and outbox for a later forward upgrade, but confirm the older binary's config parser tolerates unknown keys before starting it. If it does not, use a temporary config copy without the block and retain the original securely.
- Do not re-enable Keeper's legacy pull path while push remains enabled. If operational rollback requires legacy pull, stop/drain request traffic, disable push, confirm it is disabled, enable legacy pull, then resume traffic. Record the cutover time; the two paths have different delivery identities and any traffic admitted during an overlap/gap needs explicit review.
- A Keeper binary rollback requires the database restore procedure in the Keeper runbook; using an older binary against a migrated database is not a genuine rollback.

## Multiple CPA isolation

For every CPA:

- create a separate immutable Keeper instance;
- issue a separate credential and environment variable/file;
- use a separate local outbox path/volume;
- verify the returned instance ID before enabling;
- filter Keeper views by instance when investigating;
- never clone a live outbox to another active CPA.

Data created before multi-instance support remains in Keeper's deterministic legacy instance `00000000-0000-7000-8000-000000000000` (`Legacy`). The old pull/runtime path continues to write only to that namespace. It is not a template for new CPA registrations and its deterministic ID must not be reused for a new exporter.

## Secret and privacy handling

Never put these values in YAML, screenshots, tickets, shell history, logs, database queries, or release artifacts:

- Keeper ingest token or Authorization header;
- CPA management key;
- provider/client API keys or OAuth tokens;
- auth-file contents;
- mTLS private key contents;
- raw failure bodies or arbitrary provider headers/bodies.

CPA exports only approved secret-free projections. `request_id` is correlation-only, not deduplication. `api_key_fingerprint` is the only exported API-key identity and is private to its Keeper instance. The Keeper SQLite database, CPA outbox, and their backups still contain operational usage/metadata and must be access-controlled and encrypted at rest where required.

## Troubleshooting

| Symptom/code | Checks |
|---|---|
| `missing_credential` / token not configured | Confirm `token-env` spelling and that the CPA process, not only your shell, has the variable. Restart/recreate after env changes. |
| `invalid_credential` | Token may be mistyped, expired, rotated, or revoked. Issue/rotate for the same instance; never print it for debugging. |
| `insufficient_scope` | Use a credential with `identity:test` for test/bootstrap and `usage:push`/`metadata:push` for enabled categories. |
| `instance_disabled` | Re-enable the intended Keeper instance or keep exporter disabled during containment. |
| TLS unknown authority | Mount the correct PEM CA bundle and set `ca-file`; verify hostname/SAN and system clock. Do not enable insecure skip-verify. |
| mTLS handshake failure | Check client cert/key pairing, permissions, expiry, EKU, issuer, and Keeper/reverse-proxy client-CA policy. |
| URL rejected | Use an absolute HTTPS base URL without query, fragment, or credentials. Do not include `/api/v1`; include only the configured Keeper `APP_BASE_PATH` when applicable. |
| `rate_limited` | Honor `Retry-After`; investigate credential sharing or an overly aggressive retry/flush configuration. |
| `service_unavailable` / timeout | Check Keeper health, reverse proxy limits, route reachability, certificate chain, and request timeout. Queued data remains local. |
| `conflicting_replay` | Freeze both databases and compare stream/sequence provenance. Do not delete rows or reset stream metadata. |
| backlog grows while `connected` | Keeper can ACK a subset/contiguous prefix. Check `nextExpectedSequence`, Keeper processing health, quota, and oldest backlog age. |
| `blocked` or outbox full | Disable exporter, preserve the file, free filesystem capacity, increase `max-bytes` only after sizing, then re-enable. Events rejected after full cannot be reconstructed from the outbox. |
| metadata revision conflict/stale | Confirm only one CPA owns the outbox and category revision state; do not copy outboxes across active instances. |
| protocol/JSON error | Confirm Keeper and CPA both support `keeper-export/v1` and no proxy rewrites or compresses bodies. |

## Upgrade compatibility and staged canary

1. Confirm release notes and protocol fixtures remain `keeper-export/v1`; a new protocol version requires explicit compatibility planning.
2. Back up Keeper SQLite and each canary CPA outbox.
3. Upgrade Keeper first and verify migrations/health with exporters still disabled.
4. Upgrade one low-volume CPA and matching Management Center.
5. Run Test Connection, enable push, generate known test traffic, and verify instance identity, ACK advance, backlog drain, metadata revisions, and absence of secret values in logs.
6. Rotate the canary credential once and verify recovery with queued data intact.
7. Observe for at least the longest normal retry/backlog window used by the deployment.
8. Roll out remaining CPAs one at a time. Keep each repo independently reversible; do not require all three binaries to share one version.

## Concise migration and release checklist

- [ ] Keeper stopped and SQLite backup verified before migration.
- [ ] New Keeper started; forward migrations and health succeeded.
- [ ] One Keeper instance and one scoped credential created per CPA.
- [ ] Token stored only in private environment; CA/mTLS files owner-only.
- [ ] Durable per-CPA outbox mounted with adequate quota and free space.
- [ ] Settings saved disabled; connection test returns expected instance.
- [ ] Canary enabled; ACK advances, backlog drains, metadata revisions appear.
- [ ] Traffic drained; final legacy pull reconciled and disabled before push was enabled; binding and health verified before traffic resumed.
- [ ] Management Center deployed with explicit release VERSION.
- [ ] Backup, disable, credential rotation, outage, and rollback drills recorded.
- [ ] No secret found in config, logs, DB exports, screenshots, or artifacts.
- [ ] Each independent repository gets its own valid version tag only when an authorized push occurs.

## Independent release strategy (no release is performed by this runbook)

The umbrella directory is not a Git repository. Work, tags, and pushes occur inside each subrepository only. Never create a tag at the umbrella root.

- Tag format for all three repositories: `v<major>.<minor>.<patch>-<seq>`.
- If an upstream project has a newer base version, use that base and reset suffix to `-1`; otherwise increment only the current suffix.
- Every push to `origin main` must be paired with exactly one pushed version tag. Several commits may be included in one push, but an untagged push is forbidden.
- `CLIProxyAPIPlus`: fetch/merge upstream using the local-preservation procedure, run required Go validation and the local-only auth symbol preservation check before release.
- `cpa-usage-keeper`: independently merge/release and verify its binary workflow. The Keeper database backup is a deployment prerequisite, not a substitute for source validation.
- `Cli-Proxy-API-Management-Center`: build locally with an explicit approved tag and prove that it identifies `HEAD`:

  ```sh
  TAG='v<major>.<minor>.<patch>-<seq>'
  test "$(git rev-parse "$TAG^{commit}")" = "$(git rev-parse HEAD)"
  VERSION="$TAG" bun run build
  ```

  Never deploy a bundle that reports `dev`. The tag workflow injects `VERSION=${{ github.ref_name }}` and publishes `management.html`.
- A recommended coordinated order is Keeper tag/release, CPA tag/release, then Management Center tag/release. Tags do not need matching base numbers across repositories.
- Do not commit, tag, or push until the release operator explicitly authorizes release.
