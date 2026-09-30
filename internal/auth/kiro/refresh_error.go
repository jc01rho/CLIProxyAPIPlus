package kiro

import (
	"fmt"
	"net/http"
)

// RefreshError reports rejection by the auth host without exposing response secrets.
// Ported from kiro-lb src/auth.rs (1581af9).
type RefreshError struct {
	HTTPStatus int
}

func (e *RefreshError) Error() string {
	return fmt.Sprintf("Kiro token refresh failed (status %d)", e.HTTPStatus)
}

func (e *RefreshError) StatusCode() int { return e.HTTPStatus }

// CredentialDead distinguishes revoked credentials from transient refresh failures.
func (e *RefreshError) CredentialDead() bool {
	return e.HTTPStatus == http.StatusBadRequest || e.HTTPStatus == http.StatusUnauthorized || e.HTTPStatus == http.StatusForbidden
}
