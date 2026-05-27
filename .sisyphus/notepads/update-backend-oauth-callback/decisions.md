### Trae OAuth Callback Handling
- Updated PostOAuthCallback in CLIProxyAPIPlus/internal/api/handlers/management/oauth_callback.go to support the userJwt parameter for the Trae provider.
- Trae uses userJwt instead of code in its redirect URL query parameters.
- The extracted userJwt is saved as the code field in the .oauth-trae-{state}.oauth file, which is the format expected by the backend's waiting goroutine.
