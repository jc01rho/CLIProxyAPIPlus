package cmd

import (
	"context"
	"strings"
	"testing"
)

func TestNewAuthManagerRegistersWorkBuddyAuthenticator(t *testing.T) {
	_, _, err := newAuthManager().Login(context.Background(), "workbuddy", nil, nil)
	if err == nil {
		t.Fatal("Login() error = nil, want configuration error")
	}
	if strings.Contains(err.Error(), "not registered") {
		t.Fatalf("workbuddy authenticator was not registered: %v", err)
	}
	if !strings.Contains(err.Error(), "configuration is required") {
		t.Fatalf("Login() error = %v, want configuration validation", err)
	}
}
