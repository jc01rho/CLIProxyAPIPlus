package kiro

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestResolveKiroCLIDatabasePaths(t *testing.T) {
	t.Parallel()

	t.Run("linux", func(t *testing.T) {
		t.Parallel()
		got := ResolveKiroCLIDatabasePaths("/home/tester", "linux", map[string]string{})
		want := filepath.Join("/home/tester", ".local", "share", "kiro-cli", "data.sqlite3")
		if len(got) == 0 || got[0] != want {
			t.Fatalf("paths = %#v, want first %q", got, want)
		}
	})

	t.Run("darwin", func(t *testing.T) {
		t.Parallel()
		got := ResolveKiroCLIDatabasePaths("/Users/tester", "darwin", map[string]string{})
		want := filepath.Join("/Users/tester", "Library", "Application Support", "kiro-cli", "data.sqlite3")
		if len(got) == 0 || got[0] != want {
			t.Fatalf("paths = %#v, want first %q", got, want)
		}
	})

	t.Run("windows localappdata", func(t *testing.T) {
		t.Parallel()
		got := ResolveKiroCLIDatabasePaths(`C:\Users\tester`, "windows", map[string]string{
			"LOCALAPPDATA": `D:\Local`,
		})
		want := `D:\Local\Kiro-Cli\data.sqlite3`
		if len(got) == 0 || got[0] != want {
			t.Fatalf("paths = %#v, want first %q", got, want)
		}
	})

	t.Run("explicit selector only", func(t *testing.T) {
		t.Parallel()
		got := ResolveKiroCLIDatabasePaths("/home/tester", "linux", map[string]string{
			"KIROCLI_DB_PATH": "/tmp/selected.sqlite3",
		})
		if len(got) != 1 || got[0] != "/tmp/selected.sqlite3" {
			t.Fatalf("paths = %#v", got)
		}
	})
}

func TestLoadKiroCLICredentialSupportsCorrectOIDCKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	tokenJSON, _ := json.Marshal(map[string]any{
		"accessToken":  "access-value",
		"refreshToken": "refresh-value",
		"expiresAt":    "2030-01-02T03:04:05Z",
		"region":       "eu-west-1",
	})
	registrationJSON, _ := json.Marshal(map[string]any{
		"clientId":     "client-id",
		"clientSecret": "client-secret",
	})
	profileJSON, _ := json.Marshal(map[string]any{
		"arn": "arn:aws:codewhisperer:ap-southeast-2:123456789012:profile/test",
	})

	for _, row := range []struct {
		key   string
		value []byte
	}{
		{"kirocli:oidc:token", tokenJSON},
		{"kirocli:oidc:device-registration", registrationJSON},
	} {
		if _, err := db.Exec(`INSERT INTO auth_kv(key, value) VALUES(?, ?)`, row.key, string(row.value)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO state(key, value) VALUES(?, ?)`, "api.codewhisperer.profile", string(profileJSON)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := LoadKiroCLICredential(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "access-value" || got.RefreshToken != "refresh-value" {
		t.Fatalf("unexpected tokens: %#v", got)
	}
	if got.ClientID != "client-id" || got.ClientSecret != "client-secret" {
		t.Fatalf("missing registration: %#v", got)
	}
	if got.ProfileArn != "arn:aws:codewhisperer:ap-southeast-2:123456789012:profile/test" {
		t.Fatalf("profile ARN = %q", got.ProfileArn)
	}
	if got.APIRegion != "ap-southeast-2" || got.Region != "eu-west-1" {
		t.Fatalf("api/auth regions = %q/%q", got.APIRegion, got.Region)
	}
	if got.AuthMethod != "idc" {
		t.Fatalf("auth method = %q, want idc", got.AuthMethod)
	}
}

func TestKiroTokenDataFromMapValidatesOptionalCredentialFields(t *testing.T) {
	t.Parallel()

	validARN := "arn:aws:codewhisperer:us-gov-west-1:123456789012:profile/test"
	for _, tc := range []struct {
		name   string
		values map[string]any
		want   string
	}{
		{name: "absent fields", values: map[string]any{"accessToken": "access"}},
		{name: "null fields", values: map[string]any{"accessToken": "access", "region": nil, "profileArn": nil}},
		{name: "valid fields", values: map[string]any{"accessToken": "access", "region": "us-gov-west-1", "profileArn": validARN}, want: "us-gov-west-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := kiroTokenDataFromMap(tc.values)
			if err != nil {
				t.Fatal(err)
			}
			if got.APIRegion != tc.want {
				t.Fatalf("APIRegion = %q, want %q", got.APIRegion, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		values map[string]any
		field  string
	}{
		{name: "region wrong type", values: map[string]any{"accessToken": "access", "region": 1}, field: "region"},
		{name: "region invalid", values: map[string]any{"accessToken": "access", "region": "US-east-1"}, field: "region"},
		{name: "profile ARN wrong type", values: map[string]any{"accessToken": "access", "profileArn": 1}, field: "profileArn"},
		{name: "profile ARN malformed", values: map[string]any{"accessToken": "access", "profileArn": "arn:aws:codewhisperer:us-east-1:123:profile"}, field: "profileArn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := kiroTokenDataFromMap(tc.values)
			if err == nil {
				t.Fatal("expected error")
			}
			fieldErr := &CredentialFieldError{}
			if !errors.As(err, &fieldErr) || fieldErr.Field != tc.field {
				t.Fatalf("error = %v, want actionable error for %q", err, tc.field)
			}
		})
	}
}

func TestKiroTokenDataFromMapAcceptsRefreshTokenOnly(t *testing.T) {
	t.Parallel()

	got, err := kiroTokenDataFromMap(map[string]any{"refreshToken": "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "" || got.RefreshToken != "refresh" {
		t.Fatalf("tokens = %#v", got)
	}
	if got.ExpiresAt != time.Unix(0, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("ExpiresAt = %q, want immediately expired", got.ExpiresAt)
	}
}

func TestLoadKiroCLICredentialRejectsAmbiguousTokens(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"unknown:first:token", "unknown:second:token"} {
		if _, err := db.Exec(`INSERT INTO auth_kv(key, value) VALUES(?, ?)`, key, `{"accessToken":"token"}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadKiroCLICredential(path, ""); err == nil {
		t.Fatal("expected ambiguous token error")
	}
}
