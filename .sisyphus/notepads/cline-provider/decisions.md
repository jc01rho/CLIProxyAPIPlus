# Cline Provider - Decisions

## 2026-02-18 Task: Architecture Decisions
- Provider name: "cline" (constant in constant.go)
- Follow Kilo provider pattern for executor structure
- OAuth flow uses local callback server (like Claude provider, NOT device flow like Kilo)
- Token refresh uses RefreshLead of 5 minutes (not nil like Kilo)
- Executor targets: `https://api.cline.bot/api/v1/chat/completions`
- Auth format: `Bearer workos:{accessToken}` with workos prefix

## 2026-02-18 Task: OAuth flow compatibility decisions
- `InitiateOAuth`는 리다이렉트 자동 추적을 끄는 임시 `http.Client`를 사용해 3xx/200 응답을 모두 수용한다.
- `AuthorizeResponse`는 유지하되 `redirect_url` 필드를 확장해 JSON 응답 변형을 흡수한다.
- `ExchangeCode(ctx, code, callbackURL)`로 시그니처를 전환하여 토큰 교환 시 `redirect_uri`를 명시적으로 전달한다.
