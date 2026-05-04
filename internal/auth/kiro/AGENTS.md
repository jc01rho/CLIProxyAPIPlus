# KIRO AUTHENTICATION

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`internal/auth/kiro/` owns Kiro/AWS Builder ID auth: browser OAuth, AWS SSO OIDC device flow, token storage/refresh, jitter, cooldown, metrics, and rate limiting.

## STRUCTURE

```text
kiro/
├── oauth.go / oauth_web.go / protocol_handler.go
├── aws.go / aws_auth.go / sso_oidc.go
├── token.go / token_repository.go / refresh_manager.go
├── background_refresh.go / jitter.go
├── cooldown.go / social_auth.go / fingerprint.go
├── rate_limiter.go / usage_checker.go / metrics.go
└── *_test.go
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Browser OAuth | `oauth.go`, `oauth_web.go` | Callback/state validation. |
| AWS device flow | `aws.go`, `sso_oidc.go` | Builder ID / SSO OIDC specifics. |
| Token persistence | `token_repository.go`, `token.go` | No plaintext leakage. |
| Refresh/cooldown | `refresh_manager.go`, `background_refresh.go`, `jitter.go`, `cooldown.go` | Avoid refresh storms. |
| Rate/usage protection | `rate_limiter.go`, `usage_checker.go` | Provider safety controls. |

## CONVENTIONS

- Preserve callback state validation; do not simplify it away for UI convenience.
- Refresh logic should use background refresh and jitter when possible.
- Kiro credential-file compatibility uses `kilocodeToken` and `kilocodeOrganizationId` in Kilo paths.

## ANTI-PATTERNS

- Do not hardcode callback ports/endpoints.
- Do not add retry loops without jitter/cooldown.
- Do not log or persist tokens outside the token repository format.
