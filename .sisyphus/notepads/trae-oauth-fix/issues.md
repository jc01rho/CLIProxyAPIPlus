
## Known Issues and Gotchas - Trae.ai OAuth Implementation

### Date: 2026-01-28

## Issue: No Official Documentation

**Problem:**
- Trae.ai does not provide public OAuth documentation
- No API reference for authentication endpoints
- Implementation must be reverse-engineered

**Impact:**
- Cannot verify correctness against official spec
- Breaking changes may occur without notice
- Support requests difficult without official docs

**Workaround:**
- Document actual behavior from real callback examples
- Monitor for changes in callback parameters
- Keep reference examples in `trae-auth-flow.md`

---

## Issue: Tokens in URL Query Parameters

**Problem:**
- Access tokens and refresh tokens passed in URL query string
- URLs logged in browser history, server logs, proxy logs
- Security risk: tokens exposed in multiple places

**Impact:**
- Tokens may leak through logs
- Browser history contains sensitive data
- Proxy servers may cache URLs with tokens

**Mitigation:**
- Local callback server only (no external exposure)
- Clear browser history after authentication
- Warn users about security implications
- Consider using POST for callback (if Trae supports it)

**Note:**
This is Trae.ai's design choice, not something we can change.

---

## Issue: JSON Strings in URL Parameters

**Problem:**
- `userJwt` and `userInfo` are JSON objects encoded as URL parameters
- Must URL-decode then JSON-parse
- Double-encoding complexity

**Example:**
```
userJwt=%7B%22ClientID%22%3A%22ono9krqynydwx5%22%2C%22RefreshToken%22%3A...
```

**Gotcha:**
- Must use `query.Get()` which auto-decodes URL encoding
- Then `json.Unmarshal()` to parse JSON
- Order matters: decode URL first, then parse JSON

**Implementation:**
```go
userJwtStr := query.Get("userJwt")  // Auto URL-decodes
var userJWT UserJWT
json.Unmarshal([]byte(userJwtStr), &userJWT)  // Parse JSON
```

---

## Issue: Port Conflicts

**Problem:**
- Callback server needs available port
- Port 58972 hardcoded for Trae
- May conflict with other services

**Impact:**
- OAuth flow fails if port in use
- Error message unclear to users
- Multiple instances cannot run simultaneously

**Current Handling:**
- Check port availability before starting
- Return error if port unavailable
- User must stop conflicting service

**Potential Improvement:**
- Use dynamic port allocation
- Update callback URL with actual port
- Requires testing with Trae.ai

---

## Issue: Regional API Endpoints

**Problem:**
- API endpoint varies by user region
- Singapore: `https://api-sg-central.trae.ai`
- Other regions may have different URLs
- Must extract from callback parameters

**Impact:**
- Cannot hardcode API endpoint
- Must store `host` from callback
- API calls fail if wrong endpoint used

**Solution:**
- Store `host` parameter in auth metadata
- Use stored host for all API requests
- Update host if user changes region

---

## Issue: Token Expiration Timestamps

**Problem:**
- Trae uses millisecond timestamps (Unix epoch * 1000)
- Go typically uses second timestamps
- Easy to confuse the two

**Example:**
```
TokenExpireAt: 1770787856069  // milliseconds
```

**Gotcha:**
- Must divide by 1000 to get seconds
- Or use `time.UnixMilli()` in Go 1.17+
- Mixing units causes off-by-1000x errors

**Implementation:**
```go
expiresAt := time.UnixMilli(userJWT.TokenExpireAt)
```

---

## Issue: Device Fingerprinting Requirements

**Problem:**
- Trae requires device/machine identification
- Must generate consistent IDs across sessions
- Implementation details not documented

**Current Implementation:**
- `GenerateMachineID()`: Hardware-based hash
- `GenerateDeviceID()`: Derived from machine ID
- OS version, device type detection

**Gotcha:**
- IDs must be consistent for same machine
- Changing hardware may invalidate sessions
- Virtual machines may have unstable IDs

---

## Issue: Web UI Callback Forwarding

**Problem:**
- Web UI runs in browser, cannot receive direct callbacks
- Need intermediate server to forward callbacks
- File-based communication between processes

**Complexity:**
- Start callback forwarder on fixed port
- Forward to management API endpoint
- Write result to temporary file
- Frontend polls file for completion
- Clean up file after reading

**Gotcha:**
- Race condition: file written before poll starts
- Timeout: file never written
- Cleanup: orphaned files if process crashes

**Current Handling:**
- 500ms poll interval
- 5-minute timeout
- File cleanup on success or error

---

## Issue: State Parameter Mismatch

**Problem:**
- State parameter used for CSRF protection
- Must match between request and callback
- Easy to lose track in async flow

**Impact:**
- Security vulnerability if not validated
- User confusion if wrong session completed

**Current Validation:**
- Generate unique state per request
- Store in session map
- Validate on callback
- Clear after use

---

## Issue: Error Messages from Trae

**Problem:**
- Trae may return errors in callback
- Error format not documented
- May be in `error` parameter or missing tokens

**Current Handling:**
```go
if nativeResult.Error != "" {
    // Handle error string
}
if nativeResult.UserJWT == nil {
    // Handle missing token
}
```

**Gotcha:**
- Some errors silent (no error parameter)
- Must check for missing required fields
- User-friendly error messages needed

---

## Issue: Refresh Token Implementation

**Problem:**
- Refresh token provided but refresh endpoint unknown
- No documentation on token refresh flow
- May need to re-authenticate instead

**Current Status:**
- Refresh token stored but not used
- Token refresh not implemented
- Users must re-authenticate when token expires

**TODO:**
- Research Trae token refresh endpoint
- Implement refresh logic
- Test token refresh flow

---

## Issue: Client ID Hardcoded

**Problem:**
- Client ID "ono9krqynydwx5" hardcoded
- Appears to be Trae's official IDE client ID
- Cannot use custom client ID

**Impact:**
- All users share same client ID
- Cannot create separate OAuth app
- Rate limits may be shared

**Note:**
- This is intentional for native IDE flow
- Trae does not support custom OAuth apps for this flow

