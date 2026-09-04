package auth

import (
	"testing"
	"time"
)

func TestAuthHasCustomBaseURL(t *testing.T) {
	cases := []struct {
		name string
		auth *Auth
		want bool
	}{
		{"nil auth", nil, false},
		{"no attributes", &Auth{}, false},
		{"empty base_url", &Auth{Attributes: map[string]string{"base_url": " "}}, false},
		{"official host", &Auth{Attributes: map[string]string{"base_url": "https://api.anthropic.com"}}, false},
		{"official host case-insensitive", &Auth{Attributes: map[string]string{"base_url": "https://API.ANTHROPIC.COM/v1"}}, false},
		{"third-party mirror", &Auth{Attributes: map[string]string{"base_url": "https://claude.nekos.me"}}, true},
		{"custom compat", &Auth{Attributes: map[string]string{"base_url": "https://compat.example.com/v1"}}, true},
		{"unparseable", &Auth{Attributes: map[string]string{"base_url": "://bad"}}, true},
	}
	for _, tc := range cases {
		if got := authHasCustomBaseURL(tc.auth); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestNextRefreshCheckAt_SkipsCustomBaseURL(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       "claude-custom",
		Provider: "claude",
		Attributes: map[string]string{
			"base_url": "https://claude.nekos.me",
		},
		Metadata: map[string]any{
			"access_token":  "tok",
			"refresh_token": "ref",
		},
	}
	if _, err := manager.Register(t.Context(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := nextRefreshCheckAt(time.Now(), auth, 0); ok {
		t.Fatalf("custom base_url credential must not be scheduled for refresh")
	}
	plain := &Auth{
		ID:       "claude-official",
		Provider: "claude",
		Metadata: map[string]any{
			"access_token":  "tok",
			"refresh_token": "ref",
		},
	}
	if _, err := manager.Register(t.Context(), plain); err != nil {
		t.Fatalf("register: %v", err)
	}
	_ = plain

}

func TestRefreshAuthForRequest_SkipsCustomBaseURL(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       "claude-custom-req",
		Provider: "claude",
		Attributes: map[string]string{
			"base_url": "https://claude.nekos.me",
		},
		Metadata: map[string]any{
			"access_token":  "tok",
			"refresh_token": "ref",
		},
	}
	if _, err := manager.Register(t.Context(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := manager.refreshAuthForRequest(t.Context(), auth.ID, "tok"); err == nil {
		t.Fatalf("expected skip error for custom base_url")
	}
}
