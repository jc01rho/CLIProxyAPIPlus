# Trae.ai OAuth - Quick Reference Guide

## TL;DR

Trae.ai uses **proprietary Native IDE auth**, NOT standard OAuth2.
- No code exchange, tokens come directly in callback URL
- Requires device fingerprinting
- Regional API endpoints

---

## Authorization URL

```
https://www.trae.ai/authorization?
  login_version=1
  &auth_from=trae
  &login_channel=native_ide
  &plugin_version={version}
  &auth_type=local
  &client_id=ono9krqynydwx5
  &redirect=1
  &login_trace_id={uuid}
  &auth_callback_url=http://127.0.0.1:{port}/authorize
  &machine_id={machine_id}
  &device_id={device_id}
  &x_device_id={device_id}
  &x_machine_id={machine_id}
  &x_device_brand={brand}
  &x_device_type={os}
  &x_os_version={version}
  &x_env=
  &x_app_version={version}
  &x_app_type=stable
```

**Generate with:** `GenerateNativeAuthURL(callbackURL, appVersion)`

---

## Callback URL

```
http://127.0.0.1:{port}/authorize?
  isRedirect=true
  &scope=trae
  &data={encrypted}
  &refreshToken={token}
  &loginTraceID={uuid}
  &host={api_endpoint}
  &refreshExpireAt={timestamp_ms}
  &userRegion={region}
  &userJwt={json_string}
  &userInfo={json_string}
  &userTag={tag}
```

**Handle with:** `handleAuthorize()` in oauth_server.go

---

## UserJWT Structure

```json
{
  "ClientID": "ono9krqynydwx5",
  "RefreshToken": "base64_token",
  "RefreshExpireAt": 1785130256069,
  "Token": "eyJhbGci...",
  "TokenExpireAt": 1770787856069,
  "TokenExpireDuration": 1209600000
}
```

**Parse from:** `query.Get("userJwt")` → `json.Unmarshal()`

---

## UserInfo Structure

```json
{
  "ScreenName": "User Name",
  "UserID": "7600280551593214984",
  "NonPlainTextEmail": "user@example.com",
  "TenantID": "7o2d894p7dr0o4",
  "Region": "Singapore-Central",
  "AIRegion": "SG",
  "AvatarUrl": "https://...",
  "LastLoginType": "google"
}
```

**Parse from:** `query.Get("userInfo")` → `json.Unmarshal()`

---

## Implementation Checklist

### Starting OAuth Flow

- [ ] Generate machine_id and device_id
- [ ] Create unique login_trace_id (UUID)
- [ ] Start local callback server on port 58972
- [ ] Generate authorization URL with all parameters
- [ ] Open URL in browser
- [ ] Wait for callback (5 min timeout)

### Handling Callback

- [ ] Extract `userJwt` query parameter
- [ ] URL-decode (automatic with `query.Get()`)
- [ ] JSON-parse to UserJWT struct
- [ ] Extract `userInfo` query parameter
- [ ] JSON-parse to UserInfo struct
- [ ] Extract `host` for regional API endpoint
- [ ] Validate login_trace_id matches
- [ ] Store tokens in auth file

### Storing Auth Data

```go
metadata := map[string]any{
    "access_token":    userJWT.Token,
    "refresh_token":   userJWT.RefreshToken,
    "client_id":       userJWT.ClientID,
    "token_expire_at": userJWT.TokenExpireAt,
    "user_id":         userInfo.UserID,
    "screen_name":     userInfo.ScreenName,
    "host":            host,
    "user_region":     userRegion,
    "login_trace_id":  loginTraceID,
    "last_refresh":    time.Now().Format(time.RFC3339),
}
```

---

## Common Mistakes

❌ **Using standard OAuth2 flow** → Use native IDE flow  
❌ **Forgetting to URL-decode** → `query.Get()` does it automatically  
❌ **Treating timestamps as seconds** → They're milliseconds  
❌ **Hardcoding API endpoint** → Use `host` from callback  
❌ **Implementing PKCE** → Not used by Trae  
❌ **Expecting authorization code** → Tokens come directly  

---

## Code Examples

### Generate Auth URL

```go
import "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/trae"

callbackURL := "http://127.0.0.1:58972/authorize"
appVersion := "3.5.25"

authURL, loginTraceID, err := trae.GenerateNativeAuthURL(callbackURL, appVersion)
if err != nil {
    return err
}

// Open authURL in browser
// Wait for callback...
```

### Handle Callback

```go
func (s *OAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
    query := r.URL.Query()
    
    // Extract and parse userJwt
    userJwtStr := query.Get("userJwt")
    var userJWT UserJWT
    if err := json.Unmarshal([]byte(userJwtStr), &userJWT); err != nil {
        http.Error(w, "Invalid userJwt", http.StatusBadRequest)
        return
    }
    
    // Extract and parse userInfo
    userInfoStr := query.Get("userInfo")
    var userInfo UserInfo
    if userInfoStr != "" {
        json.Unmarshal([]byte(userInfoStr), &userInfo)
    }
    
    // Build result
    result := &NativeOAuthResult{
        UserJWT:      &userJWT,
        UserInfo:     &userInfo,
        Host:         query.Get("host"),
        UserRegion:   query.Get("userRegion"),
        LoginTraceID: query.Get("loginTraceID"),
    }
    
    // Send to waiting channel
    s.sendNativeResult(result)
    
    // Redirect to success page
    http.Redirect(w, r, "/success", http.StatusFound)
}
```

### Wait for Callback

```go
server := trae.NewOAuthServer(58972)
if err := server.Start(); err != nil {
    return err
}
defer server.Stop(context.Background())

result, err := server.WaitForNativeCallback(5 * time.Minute)
if err != nil {
    return err
}

// Use result.UserJWT.Token as access token
// Use result.Host as API endpoint
```

---

## Testing

### Manual Test

1. Run callback server: `go run cmd/server/main.go`
2. Generate auth URL with correct parameters
3. Open in browser
4. Authenticate with Trae.ai
5. Verify callback received with all parameters
6. Check tokens stored correctly

### Verify Callback

```bash
# Check if callback file created (Web UI mode)
ls -la ~/.cliproxy/auth/.oauth-trae-*.oauth

# Check auth file created
ls -la ~/.cliproxy/auth/trae-*.json

# View auth file contents (mask tokens!)
cat ~/.cliproxy/auth/trae-*.json | jq '.metadata'
```

---

## Debugging

### Enable Debug Logging

```go
import log "github.com/sirupsen/logrus"
log.SetLevel(log.DebugLevel)
```

### Check Callback Parameters

```go
log.Debugf("Callback query: %v", r.URL.Query())
log.Debugf("userJwt length: %d", len(query.Get("userJwt")))
log.Debugf("host: %s", query.Get("host"))
log.Debugf("userRegion: %s", query.Get("userRegion"))
```

### Common Issues

**Port in use:** Check with `lsof -i :58972`  
**Timeout:** Check browser opened, user authenticated  
**Invalid JSON:** Check URL decoding happened  
**Missing tokens:** Check userJwt parameter present  
**Wrong API endpoint:** Check host parameter extracted  

---

## References

- **Implementation:** `internal/auth/trae/`
- **Examples:** `trae-auth-flow.md`
- **Full Docs:** `.sisyphus/notepads/trae-oauth-fix/`

