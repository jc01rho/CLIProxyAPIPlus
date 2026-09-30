package kiro

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestKiroCredentialRegionsRejectMalformedPresentFields(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"region", "apiRegion", "api_region"} {
		for _, invalid := range []any{false, 42, []any{"us-east-1"}, "", "US-EAST-1", " us-east-1", "us-east-1 ", "us-east-0", "us-east-01", "us-east", "us-east-1/evil", "us-east-1.example", "u-east-1"} {
			_, err := kiroTokenDataFromMap(map[string]any{"accessToken": "token", field: invalid})
			var fieldErr *CredentialFieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != field {
				t.Errorf("%s=%v: error = %v, want CredentialFieldError", field, invalid, err)
			}
		}
	}
	for _, field := range []string{"arn", "profileArn", "profile_arn"} {
		for _, invalid := range []any{false, 42, map[string]any{}, "", "secret-arn", "arn:aws:s3:us-east-1:123:profile/test", "arn:aws:codewhisperer::123:profile/test", "arn:aws:codewhisperer:US-EAST-1:123:profile/test", "arn:aws:codewhisperer:us-east-1:123:profile/", "arn:aws:codewhisperer:us-east-1:123:profile/test/extra"} {
			_, err := kiroTokenDataFromMap(map[string]any{"refreshToken": "refresh", field: invalid})
			var fieldErr *CredentialFieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != field {
				t.Errorf("%s=%v: error = %v, want CredentialFieldError", field, invalid, err)
			}
		}
	}
	_, err := kiroTokenDataFromMap(map[string]any{"accessToken": "token", "profileArn": "arn:aws:codewhisperer:us-east-1:123:profile/test", "profile_arn": false})
	if err == nil {
		t.Fatal("a valid alias must not hide a malformed present alias")
	}
}

func TestKiroCredentialRegionsAcceptFuturePartitions(t *testing.T) {
	t.Parallel()
	for _, region := range []string{"us-east-1", "us-gov-west-1", "us-isob-east-1", "eusc-de-east-1", "ap-southeast-12"} {
		got, err := kiroTokenDataFromMap(map[string]any{"accessToken": "token", "region": region})
		if err != nil || got.Region != region || got.APIRegion != region {
			t.Fatalf("region %q: got %v, err %v", region, got, err)
		}
	}
}

func TestKiroCredentialFileImportsRefreshOnlyAndValidateFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, KiroIDETokenFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{
		`{"refreshToken":"refresh","expiresAt":"2099-01-01T00:00:00Z"}`,
		`{"refresh_token":"refresh","access_token":"","expires_at":"2099-01-01T00:00:00Z","region":null,"profileArn":null}`,
	} {
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		for name, load := range map[string]func() (*KiroTokenData, error){
			"IDE":    LoadKiroIDEToken,
			"custom": func() (*KiroTokenData, error) { return LoadKiroTokenFromPath(path) },
			"JSON":   func() (*KiroTokenData, error) { return loadKiroCredentialJSON(path) },
		} {
			got, err := load()
			if err != nil || got.RefreshToken != "refresh" || got.AccessToken != "" || got.ExpiresAt != "1970-01-01T00:00:00Z" {
				t.Fatalf("%s: token=%v err=%v", name, got, err)
			}
		}
	}
	if err := os.WriteFile(path, []byte(`{"accessToken":"access","region":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, load := range []func() (*KiroTokenData, error){LoadKiroIDEToken, func() (*KiroTokenData, error) { return LoadKiroTokenFromPath(path) }} {
		_, err := load()
		var fieldErr *CredentialFieldError
		if !errors.As(err, &fieldErr) || fieldErr.Field != "region" {
			t.Fatalf("error = %v, want region validation", err)
		}
	}
}

func TestKiroSQLiteRefreshOnlyImportAndProfileValidation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, statement := range []string{
		`CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO auth_kv VALUES ('kirocli:social:token', '{"refreshToken":"refresh","expiresAt":"2099-01-01T00:00:00Z"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadKiroCLICredential(path, "")
	if err != nil || got.AuthMethod != "social" || got.ExpiresAt != "1970-01-01T00:00:00Z" {
		t.Fatalf("token = %v, err = %v", got, err)
	}
	if _, err := db.Exec(`INSERT INTO state VALUES ('api.codewhisperer.profile', '{"arn":123}')`); err != nil {
		t.Fatal(err)
	}
	_, err = LoadKiroCLICredential(path, "")
	var fieldErr *CredentialFieldError
	if !errors.As(err, &fieldErr) || fieldErr.Field != "arn" {
		t.Fatalf("error = %v, want ARN validation", err)
	}
}
