package kiro

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

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
