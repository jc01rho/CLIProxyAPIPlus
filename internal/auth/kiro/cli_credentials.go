package kiro

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultImportedKiroCredentialTTL = time.Hour

var (
	kiroCLITokenKeys = []string{
		"kirocli:odic:token",
		"kirocli:oidc:token",
		"kirocli:social:token",
		"codewhisperer:odic:token",
	}
	kiroCLIRegistrationKeys = []string{
		"kirocli:odic:device-registration",
		"kirocli:oidc:device-registration",
		"codewhisperer:odic:device-registration",
	}
)

// ResolveKiroCLIDatabasePaths returns Kiro CLI credential stores in import
// precedence order. An explicit KIROCLI_DB_PATH/KIRO_CLI_DB_FILE selector
// replaces native and compatibility fallbacks.
func ResolveKiroCLIDatabasePaths(home, goos string, env map[string]string) []string {
	if configured := firstNonBlank(env["KIROCLI_DB_PATH"], env["KIRO_CLI_DB_FILE"]); configured != "" {
		return []string{expandKiroCredentialPath(configured, home)}
	}

	var native string
	switch goos {
	case "windows":
		base := firstNonBlank(env["LOCALAPPDATA"], windowsLocalAppData(env["USERPROFILE"]), windowsLocalAppData(home))
		native = joinWindowsPath(base, "Kiro-Cli", "data.sqlite3")
	case "darwin":
		native = filepath.Join(home, "Library", "Application Support", "kiro-cli", "data.sqlite3")
	default:
		native = filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3")
	}

	return []string{
		native,
		filepath.Join(home, ".local", "share", "amazon-q", "data.sqlite3"),
		filepath.Join(home, ".kiro", "sso", "cache.db"),
	}
}

// LoadImportedKiroCredential discovers credentials from explicit JSON files,
// Kiro CLI SQLite stores, and their native compatibility fallbacks.
func LoadImportedKiroCredential() (*KiroTokenData, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("kiro credential import: resolve home directory: %w", err)
	}

	env := environmentMap(os.Environ())
	for _, configured := range []string{env["KIRO_CREDS_FILE"], env["KIRO_CREDENTIALS_FILE"]} {
		if strings.TrimSpace(configured) == "" {
			continue
		}
		token, loadErr := loadKiroCredentialJSON(expandKiroCredentialPath(configured, home))
		if loadErr == nil {
			token.Source = "json"
			return token, nil
		}
		if !errors.Is(loadErr, os.ErrNotExist) {
			return nil, loadErr
		}
	}

	var failures []string
	for _, path := range ResolveKiroCLIDatabasePaths(home, runtime.GOOS, env) {
		token, loadErr := LoadKiroCLICredential(path, env["KIROCLI_TOKEN_KEY"])
		if loadErr == nil {
			token.Source = "sqlite"
			return token, nil
		}
		if errors.Is(loadErr, os.ErrNotExist) {
			continue
		}
		failures = append(failures, loadErr.Error())
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("kiro credential import failed: %s", strings.Join(failures, "; "))
	}
	return nil, os.ErrNotExist
}

// LoadKiroCLICredential loads one Kiro CLI SQLite credential without logging
// token values, registration secrets, profile ARNs, or raw database rows.
func LoadKiroCLICredential(path, selector string) (*KiroTokenData, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}

	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("kiro credential database unreadable")
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return nil, fmt.Errorf("kiro credential database unreadable")
	}

	tokenKey, tokenValue, err := selectKiroCLIToken(ctx, db, strings.TrimSpace(selector))
	if err != nil {
		return nil, err
	}

	tokenMap, err := decodeKiroCredentialJSON(tokenValue)
	if err != nil {
		return nil, fmt.Errorf("kiro credential database token is invalid JSON")
	}

	registrationMap := map[string]any{}
	for _, key := range kiroCLIRegistrationKeys {
		var value string
		queryErr := db.QueryRowContext(ctx, "SELECT value FROM auth_kv WHERE key = ?", key).Scan(&value)
		if errors.Is(queryErr, sql.ErrNoRows) {
			continue
		}
		if queryErr != nil {
			return nil, fmt.Errorf("kiro credential database schema mismatch")
		}
		registrationMap, err = decodeKiroCredentialJSON(value)
		if err != nil {
			return nil, fmt.Errorf("kiro credential database registration is invalid JSON")
		}
		break
	}

	profileMap := map[string]any{}
	var profileValue string
	if queryErr := db.QueryRowContext(ctx, "SELECT value FROM state WHERE key = ?", "api.codewhisperer.profile").Scan(&profileValue); queryErr == nil {
		profileMap, _ = decodeKiroCredentialJSON(profileValue)
	} else if !errors.Is(queryErr, sql.ErrNoRows) && !strings.Contains(strings.ToLower(queryErr.Error()), "no such table") {
		return nil, fmt.Errorf("kiro credential database schema mismatch")
	}

	merged := make(map[string]any, len(registrationMap)+len(tokenMap)+len(profileMap))
	for _, source := range []map[string]any{registrationMap, tokenMap, profileMap} {
		for key, value := range source {
			merged[key] = value
		}
	}

	token, err := kiroTokenDataFromMap(merged)
	if err != nil {
		return nil, err
	}
	token.Source = "sqlite"
	if token.AuthMethod == "" {
		switch {
		case strings.Contains(tokenKey, ":social:"):
			token.AuthMethod = "social"
		case token.ProfileArn != "":
			token.AuthMethod = "idc"
		default:
			token.AuthMethod = "builder-id"
		}
	}
	return token, nil
}

func selectKiroCLIToken(ctx context.Context, db *sql.DB, selector string) (string, string, error) {
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM auth_kv WHERE key LIKE ? ORDER BY key ASC", "%:token")
	if err != nil {
		return "", "", fmt.Errorf("kiro credential database schema mismatch")
	}
	defer rows.Close()

	values := make(map[string]string)
	var orderedKeys []string
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return "", "", fmt.Errorf("kiro credential database schema mismatch")
		}
		values[key] = value
		orderedKeys = append(orderedKeys, key)
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("kiro credential database unreadable")
	}

	if selector != "" {
		value, ok := values[selector]
		if !ok {
			return "", "", fmt.Errorf("KIROCLI_TOKEN_KEY selection was not found")
		}
		return selector, value, nil
	}
	for _, key := range kiroCLITokenKeys {
		if value, ok := values[key]; ok {
			return key, value, nil
		}
	}
	switch len(orderedKeys) {
	case 0:
		return "", "", fmt.Errorf("kiro credential database token missing")
	case 1:
		key := orderedKeys[0]
		return key, values[key], nil
	default:
		return "", "", fmt.Errorf("kiro credential database contains multiple tokens; set KIROCLI_TOKEN_KEY")
	}
}

func loadKiroCredentialJSON(path string) (*KiroTokenData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values, err := decodeKiroCredentialJSON(string(data))
	if err != nil {
		return nil, fmt.Errorf("kiro credential file is invalid JSON")
	}
	return kiroTokenDataFromMap(values)
}

func decodeKiroCredentialJSON(value string) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func kiroTokenDataFromMap(values map[string]any) (*KiroTokenData, error) {
	accessToken := kiroStringField(values, "accessToken", "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("kiro credential token missing")
	}
	refreshToken := kiroStringField(values, "refreshToken", "refresh_token")
	profileArn := kiroStringField(values, "arn", "profileArn", "profile_arn")
	region := normalizeKiroRegion(kiroStringField(values, "region"))
	apiRegion := normalizeKiroRegion(firstNonBlank(
		kiroStringField(values, "apiRegion", "api_region"),
		ExtractRegionFromProfileArn(profileArn),
		region,
	))

	expiresAt := parseImportedKiroExpiry(values, refreshToken != "")
	clientID := kiroStringField(values, "clientId", "client_id")
	clientSecret := kiroStringField(values, "clientSecret", "client_secret")
	authMethod := strings.ToLower(kiroStringField(values, "authMethod", "auth_method"))
	if authMethod == "" && clientID != "" && clientSecret != "" {
		if profileArn != "" {
			authMethod = "idc"
		} else {
			authMethod = "builder-id"
		}
	}

	provider := kiroStringField(values, "provider")
	if provider == "" {
		if authMethod == "social" {
			provider = "Kiro"
		} else {
			provider = "AWS"
		}
	}

	return &KiroTokenData{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ProfileArn:   profileArn,
		ExpiresAt:    expiresAt,
		AuthMethod:   authMethod,
		Provider:     provider,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		ClientIDHash: kiroStringField(values, "clientIdHash", "client_id_hash"),
		Email:        kiroStringField(values, "email"),
		StartURL:     kiroStringField(values, "startUrl", "start_url"),
		Region:       region,
		APIRegion:    apiRegion,
	}, nil
}

func parseImportedKiroExpiry(values map[string]any, hasRefreshToken bool) string {
	value, present := values["expiresAt"]
	if !present {
		value, present = values["expires_at"]
	}

	var parsed time.Time
	switch typed := value.(type) {
	case float64:
		seconds := int64(typed)
		if seconds > 10_000_000_000 {
			seconds /= 1000
		}
		parsed = time.Unix(seconds, 0)
	case json.Number:
		if numeric, err := strconv.ParseInt(string(typed), 10, 64); err == nil {
			if numeric > 10_000_000_000 {
				numeric /= 1000
			}
			parsed = time.Unix(numeric, 0)
		}
	case string:
		parsed, _ = time.Parse(time.RFC3339, typed)
		if parsed.IsZero() {
			if millis, err := strconv.ParseInt(typed, 10, 64); err == nil {
				if millis > 10_000_000_000 {
					millis /= 1000
				}
				parsed = time.Unix(millis, 0)
			}
		}
	}

	if parsed.IsZero() {
		if present && hasRefreshToken {
			return time.Unix(0, 0).UTC().Format(time.RFC3339)
		}
		parsed = time.Now().Add(defaultImportedKiroCredentialTTL)
	}
	return parsed.UTC().Format(time.RFC3339)
}

func kiroStringField(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func normalizeKiroRegion(region string) string {
	region = strings.TrimSpace(strings.ToLower(region))
	parts := strings.Split(region, "-")
	if len(parts) < 3 || len(parts[0]) != 2 {
		return ""
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
				return ""
			}
		}
	}
	last := parts[len(parts)-1]
	if len(last) != 1 || last[0] < '0' || last[0] > '9' {
		return ""
	}
	return region
}

// NormalizeKiroRegion validates a region before it is interpolated into an
// authentication or runtime endpoint.
func NormalizeKiroRegion(region string) string {
	return normalizeKiroRegion(region)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func expandKiroCredentialPath(value, home string) string {
	value = strings.TrimSpace(value)
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		return filepath.Join(home, value[2:])
	}
	return value
}

func windowsLocalAppData(home string) string {
	home = strings.TrimRight(strings.TrimSpace(home), `\/`)
	if home == "" {
		return ""
	}
	return home + `\AppData\Local`
}

func joinWindowsPath(base string, parts ...string) string {
	result := strings.TrimRight(strings.TrimSpace(base), `\/`)
	for _, part := range parts {
		part = strings.Trim(part, `\/`)
		if part != "" {
			result += `\` + part
		}
	}
	return result
}

func environmentMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
