## Patterns and Conventions
- Followed GitHub Copilot authenticator pattern for Kilocode implementation in sdk/auth/kilocode.go.
- Implementation uses device flow and registers the authenticator for the kilocode provider.

## Verification
- go build ./sdk/auth/ succeeded.
- lsp_diagnostics reported no errors in the newly created file.
