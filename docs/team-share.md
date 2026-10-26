# Team share

Pools several Claude Team seat OAuth credentials, converts each request to API-equivalent USD, and
lets each client API key use a fixed percentage of the pool per rate-limit window.

## How it works

- **Accounting.** Every Claude request adds its tokens (input, output, cache read, cache write) per model to the
  account that served it and to the API key that sent it. USD comes from the public price table, overridable with
  `team-share.pricing`.
- **Windows.** The 5h and weekly utilization and reset time are read from the
  `Anthropic-Ratelimit-Unified-{5h,7d}-{Utilization,Reset}` response headers. The estimated limit of an account is
  window cost divided by utilization (when the window start was not seen, the cost and utilization deltas since
  the first observation). Closed windows fold into a moving average; until one exists the live estimate is shown as
  `provisional`, and before any signal the `tiers` default applies.
- **Pool total** = sum of the limits of active accounts. A disabled account, one that returned 401/403, and one whose
  new estimate differs from the calibration by more than 3x (plan change) leave the total and are shown as
  `reauth_required`. Nothing logs in again automatically.
- **Member share** = total x percent per window; the pool window resets at the earliest account reset seen when the
  window started and then every 5h / 7d. Over the share, generation requests get `429`, `Retry-After` and a JSON
  error with `allocated_usd`, `used_usd`, `remaining_usd`, `remaining_percent`, `reset_at`. Keys outside every
  pool and read-only (GET) requests are never limited. Usage is counted after a request finishes, so a member can
  overshoot by the cost of the requests in flight.
- **Routing.** With team-share enabled the selector prefers the pool account with the most remaining window (accounts
  within 2 points count as equal and share load through the configured strategy and credential weights). Enforcement
  and account choice are independent. Not applied with the `weight-robin` strategy.

## Management API

- `GET /v0/management/team-share` pool totals, per-account utilization / reset / estimated limit / tokens by model,
  per-member allocation, usage and remaining.
- `PUT|DELETE /v0/management/team-share/pools/:pool/members/:name`, `PUT|DELETE .../accounts` edit the pool and
  persist the config (the 100% cap is enforced; a raw `api_key` is stored as its `key-id`).
- `POST /v0/management/team-share/ack-reauth?auth_id=` clears a re-authentication flag and restarts calibration.
- `/team-share.html` is a one-page table of the same data (asks for the management key).

State is kept in `team-share-state.json` and the audit log in `team-share-audit.log` next to the config file
(`state-file` / `audit-log` override).

## team-member

`team-member join <name> --tier standard|premium --share <percent>` and `team-member leave <name>` print the plan
and change nothing unless `--confirm` is given. `team-member check <name>` forces a token refresh of the member's
credential and prints how the pool sees the account, to confirm after a seat reassignment that the stored refresh
token still works.

join: member address, claude.ai seat, OAuth registration, API key, pool account and share, read-back of the total.
leave: API key revoked and share released (left unassigned), credential file deleted, account removed from the pool,
seat returned last. Every step is appended to the audit log (who, when, which seat) without secrets.

The claude.ai seat endpoints are supplied as data in `TEAM_MEMBER_SEAT_CONFIG` (see `lifecycle.SeatEndpoints`);
capture one real seat change in the admin console to confirm the paths and body before the first `--confirm`.
