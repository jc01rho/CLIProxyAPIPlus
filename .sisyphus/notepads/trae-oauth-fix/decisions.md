
## Architectural Decisions for Trae.ai OAuth Implementation

### Date: 2026-01-28

## Decision: Use Native IDE OAuth Flow (Not Standard OAuth2)

### Context
Trae.ai does not use standard OAuth2 authorization code flow. Instead, it uses a proprietary "Native IDE" authentication flow where tokens are delivered directly in the callback URL.

### Decision
Implement Trae authentication using the native IDE flow pattern:
1. Generate authorization URL with device fingerprinting
2. Start local callback server on `/authorize` endpoint
3. Parse `userJwt` and `userInfo` JSON strings from query parameters
4. Extract tokens directly without code exchange

### Rationale
- This is how Trae.ai's official IDE integration works
- No public OAuth2 endpoints available for standard flow
- Existing codebase already has working implementation
- Simpler than standard OAuth2 (no code exchange step)

### Consequences
- Cannot use standard OAuth2 libraries
- Custom implementation required
- Less portable to other OAuth2 providers
- But: Works reliably with Trae.ai's actual implementation

---

## Decision: Support Both Web UI and CLI Modes

### Context
The application needs to work in two different contexts:
1. Web UI: Management interface running in browser
2. CLI: Command-line tool running in terminal

### Decision
Implement dual-mode OAuth handling:
- **Web UI Mode**: Use callback forwarder + file-based communication
- **CLI Mode**: Use direct local server + channel-based communication

### Rationale
- Web UI cannot directly receive callbacks (CORS, port conflicts)
- CLI can run local server without restrictions
- File-based communication works across process boundaries
- Channel-based communication is more efficient for single process

### Implementation
```go
if isWebUI {
    // Start callback forwarder on fixed port
    // Write result to .oauth-trae-{state}.oauth file
    // Frontend polls for completion
} else {
    // Start OAuth server directly
    // Wait for callback via channel
    // Return result immediately
}
```

---

## Decision: Store Complete Token Metadata

### Context
Trae.ai provides extensive metadata in the callback including:
- User information (name, email, avatar)
- Regional API endpoints
- Token expiration timestamps
- Device/tenant information

### Decision
Store all metadata in auth file for future use:
```json
{
  "access_token": "...",
  "refresh_token": "...",
  "client_id": "...",
  "token_expire_at": 1770787856069,
  "user_id": "...",
  "screen_name": "...",
  "host": "https://api-sg-central.trae.ai",
  "user_region": "sg",
  "login_trace_id": "...",
  "last_refresh": "2026-01-28T..."
}
```

### Rationale
- Regional API endpoints vary by user
- Token expiration needed for refresh logic
- User info useful for display/debugging
- Login trace ID helps with support issues

---

## Decision: Use Device Fingerprinting

### Context
Trae.ai requires device identification parameters in authorization URL.

### Decision
Implement device fingerprinting functions:
- `GenerateMachineID()`: Hardware-based identifier
- `GenerateDeviceID()`: Derived from machine ID
- Include OS version, device type, brand

### Rationale
- Required by Trae.ai for security
- Helps prevent unauthorized access
- Consistent with official IDE implementation
- Enables device-based session management

---

## Decision: No PKCE Implementation

### Context
Standard OAuth2 best practices recommend PKCE for public clients.

### Decision
Do NOT implement PKCE for Trae authentication.

### Rationale
- Trae.ai does not support PKCE
- Tokens delivered directly in callback URL
- No code exchange step where PKCE would apply
- Adding PKCE would break compatibility

### Note
The codebase has PKCE implementation for other providers (Claude, Gemini) but it's not used for Trae.

---

## Decision: 5-Minute Callback Timeout

### Context
Need to determine how long to wait for OAuth callback.

### Decision
Use 5-minute timeout for callback waiting.

### Rationale
- User needs time to authenticate in browser
- May need to create account or verify email
- Too short: frustrating user experience
- Too long: resources held unnecessarily
- 5 minutes is industry standard

---

## Decision: Sanitize Email for Filename

### Context
Auth files named using user email, but emails contain special characters.

### Decision
Sanitize email for filename:
- Replace `@` with `_`
- Replace `.` with `_`
- Fallback to user ID if email unavailable
- Fallback to timestamp if both unavailable

### Rationale
- Filesystem-safe filenames
- Predictable naming pattern
- Easy to identify user's auth file
- Handles edge cases gracefully

## Trae OAuth State Bug Fix
- Made 'state' optional in 'submitCallback' to avoid breaking other providers that might not use it yet or use different flows.
