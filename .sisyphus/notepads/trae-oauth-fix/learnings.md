
## Trae.ai OAuth Flow Documentation - Research Findings

### Date: 2026-01-28

## Official Trae.ai OAuth Flow (Native IDE Authentication)

### 1. Authorization URL Structure
**Endpoint:** `https://www.trae.ai/authorization`

**Required Parameters:**
- `login_version`: "1"
- `auth_from`: "trae"
- `login_channel`: "native_ide"
- `plugin_version`: App version (e.g., "2.3.6266")
- `auth_type`: "local"
- `client_id`: "ono9krqynydwx5" (Trae's official client ID)
- `redirect`: "1"
- `login_trace_id`: UUID for tracking
- `auth_callback_url`: Local callback URL (e.g., "http://127.0.0.1:58972/authorize")
- `machine_id`: Generated machine fingerprint
- `device_id`: Generated device ID
- `x_device_id`: Same as device_id
- `x_machine_id`: Same as machine_id
- `x_device_brand`: Device brand/model
- `x_device_type`: OS type (e.g., "windows")
- `x_os_version`: OS version (e.g., "Windows 11 Pro")
- `x_env`: Environment (empty string)
- `x_app_version`: App version
- `x_app_type`: "stable"

### 2. Callback URL Structure
**Endpoint:** `http://127.0.0.1:{port}/authorize`

**Callback Parameters (Query String):**
- `isRedirect`: "true"
- `scope`: "trae"
- `data`: Encrypted data string
- `refreshToken`: Refresh token string
- `loginTraceID`: Login trace ID (matches request)
- `host`: API host URL (e.g., "https://api-sg-central.trae.ai")
- `refreshExpireAt`: Refresh token expiration timestamp (milliseconds)
- `userRegion`: User's region (e.g., "sg")
- `userJwt`: **JSON string** containing:
  - `ClientID`: Client ID
  - `RefreshToken`: Refresh token
  - `RefreshExpireAt`: Expiration timestamp
  - `Token`: JWT access token
  - `TokenExpireAt`: Token expiration timestamp
  - `TokenExpireDuration`: Duration in milliseconds
- `userInfo`: **JSON string** containing:
  - `ScreenName`: User's display name
  - `Gender`: Gender code
  - `AvatarUrl`: Profile picture URL
  - `UserID`: User ID
  - `Description`: User description
  - `TenantID`: Tenant ID
  - `RegisterTime`: Registration timestamp
  - `LastLoginTime`: Last login timestamp
  - `LastLoginType`: Login method (e.g., "google")
  - `Region`: Region name
  - `AIRegion`: AI region code
  - `NonPlainTextEmail`: User email
  - `StoreCountry`: Store country code
  - `StoreCountrySrc`: Source of country info
- `userTag`: User tag (e.g., "row")

### 3. Key Differences from Standard OAuth2

**NOT a standard OAuth2 flow:**
- No authorization code exchange
- No PKCE (code_challenge/code_verifier)
- Direct token delivery via callback URL
- Tokens passed as URL-encoded JSON strings in query parameters

**Native IDE Flow:**
- User opens authorization URL in browser
- User authenticates on Trae.ai website
- Trae.ai redirects to local callback server with all tokens
- Local server extracts tokens from query parameters

### 4. Token Structure

**UserJWT Object:**
```json
{
  "ClientID": "ono9krqynydwx5",
  "RefreshToken": "base64_encoded_token",
  "RefreshExpireAt": 1785130256069,
  "Token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
  "TokenExpireAt": 1770787856069,
  "TokenExpireDuration": 1209600000
}
```

**Token Expiration:**
- Access token: ~14 days (1209600000 ms)
- Refresh token: Longer duration

### 5. Implementation Files

**Core Implementation:**
- `internal/auth/trae/trae_native_oauth.go`: Generates authorization URL
- `internal/auth/trae/oauth_server.go`: Local callback server
- `internal/api/handlers/management/auth_files.go`: OAuth flow orchestration

**Key Functions:**
- `GenerateNativeAuthURL()`: Creates authorization URL with all parameters
- `handleAuthorize()`: Processes callback with userJwt and userInfo
- `WaitForNativeCallback()`: Waits for callback result

### 6. Callback Server Behavior

**Endpoints:**
- `/authorize`: Receives Trae native OAuth callback
- `/callback`: Standard OAuth callback (not used for Trae)
- `/success`: Success page displayed to user

**Flow:**
1. Start local HTTP server on available port
2. Generate authorization URL with callback URL
3. Open URL in browser
4. Wait for callback with timeout (5 minutes)
5. Extract userJwt and userInfo from query parameters
6. Parse JSON strings from URL parameters
7. Store tokens in auth file
8. Redirect user to success page

### 7. Error Handling

**Common Errors:**
- `no_user_jwt`: Missing userJwt parameter
- `invalid_user_jwt`: Failed to parse userJwt JSON
- Timeout: No callback received within 5 minutes

### 8. Security Considerations

**Machine/Device Fingerprinting:**
- `machine_id`: Generated from hardware characteristics
- `device_id`: Derived from machine_id
- Used for device tracking and security

**Token Storage:**
- Tokens stored in JSON files in auth directory
- Filename format: `trae-{sanitized_email}.json`
- Contains access_token, refresh_token, metadata

### 9. Regional API Endpoints

**Host URLs (region-specific):**
- Singapore: `https://api-sg-central.trae.ai`
- Other regions may have different endpoints
- Host URL provided in callback parameters

### 10. No Official Public Documentation

**Findings:**
- No official Trae.ai OAuth documentation found
- Implementation reverse-engineered from:
  - Actual callback URL examples in `trae-auth-flow.md`
  - Existing codebase implementation
  - Native IDE authentication flow
- Trae.ai uses proprietary authentication flow
- Not compatible with standard OAuth2 libraries

### 11. Comparison with Other Providers

**Unlike Claude/Gemini/OpenAI:**
- No authorization code exchange step
- No token endpoint for code exchange
- Tokens delivered directly in callback URL
- No client secret required
- Simpler but less standard flow

**Similar to:**
- Some desktop application OAuth flows
- Custom authentication schemes
- Direct token delivery patterns

### 12. Web UI vs CLI Mode

**Web UI Mode:**
- Uses callback forwarder on port 58972
- Forwards to management API callback endpoint
- Writes result to `.oauth-trae-{state}.oauth` file
- Frontend polls for completion

**CLI Mode:**
- Starts local OAuth server directly
- Waits for callback via channel
- No file-based communication
- Direct result delivery

## Trae OAuth State Bug Fix
- Trae OAuth callback requires 'state' parameter which was being dropped in the frontend.
- Added optional 'state' parameter to 'oauthApi.submitCallback' to support this.
- Updated 'TraeSection' to preserve state from 'startAuth' and pass it back in 'submitCallback'.
