package kilocode

import (
	"errors"
	"fmt"
	"net/http"
)

// AuthenticationError represents authentication-related errors for Kilocode.
type AuthenticationError struct {
	// Type is the type of authentication error.
	Type string `json:"type"`
	// Message is a human-readable message describing the error.
	Message string `json:"message"`
	// Code is the HTTP status code associated with the error.
	Code int `json:"code"`
	// Cause is the underlying error that caused this authentication error.
	Cause error `json:"-"`
}

// Error returns a string representation of the authentication error.
func (e *AuthenticationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// Unwrap returns the underlying cause of the error.
func (e *AuthenticationError) Unwrap() error {
	return e.Cause
}

// Common authentication error types for Kilocode device flow.
var (
	// ErrDeviceCodeFailed represents an error when requesting the device code fails.
	ErrDeviceCodeFailed = &AuthenticationError{
		Type:    "device_code_failed",
		Message: "Failed to request device code from Kilocode",
		Code:    http.StatusBadRequest,
	}

	// ErrDeviceCodeExpired represents an error when the device code has expired.
	ErrDeviceCodeExpired = &AuthenticationError{
		Type:    "device_code_expired",
		Message: "Device code has expired. Please try again.",
		Code:    http.StatusGone,
	}

	// ErrAuthorizationPending represents a pending authorization state (not an error, used for polling).
	ErrAuthorizationPending = &AuthenticationError{
		Type:    "authorization_pending",
		Message: "Authorization is pending. Waiting for user to authorize.",
		Code:    http.StatusAccepted,
	}

	// ErrAccessDenied represents an error when the user denies authorization.
	ErrAccessDenied = &AuthenticationError{
		Type:    "access_denied",
		Message: "User denied authorization",
		Code:    http.StatusForbidden,
	}

	// ErrPollingTimeout represents an error when polling times out.
	ErrPollingTimeout = &AuthenticationError{
		Type:    "polling_timeout",
		Message: "Timeout waiting for user authorization",
		Code:    http.StatusRequestTimeout,
	}

	// ErrTokenExchangeFailed represents an error when token exchange fails.
	ErrTokenExchangeFailed = &AuthenticationError{
		Type:    "token_exchange_failed",
		Message: "Failed to exchange device code for access token",
		Code:    http.StatusBadRequest,
	}

	// ErrUserInfoFailed represents an error when fetching user info fails.
	ErrUserInfoFailed = &AuthenticationError{
		Type:    "user_info_failed",
		Message: "Failed to fetch Kilocode user information",
		Code:    http.StatusBadRequest,
	}
)

// NewAuthenticationError creates a new authentication error with a cause based on a base error.
func NewAuthenticationError(baseErr *AuthenticationError, cause error) *AuthenticationError {
	return &AuthenticationError{
		Type:    baseErr.Type,
		Message: baseErr.Message,
		Code:    baseErr.Code,
		Cause:   cause,
	}
}

// IsAuthenticationError checks if an error is an authentication error.
func IsAuthenticationError(err error) bool {
	var authenticationError *AuthenticationError
	ok := errors.As(err, &authenticationError)
	return ok
}

// GetUserFriendlyMessage returns a user-friendly error message based on the error type.
func GetUserFriendlyMessage(err error) string {
	var authErr *AuthenticationError
	if errors.As(err, &authErr) {
		switch authErr.Type {
		case "device_code_failed":
			return "Failed to start Kilocode authentication. Please check your network connection and try again."
		case "device_code_expired":
			return "The authentication code has expired. Please try again."
		case "authorization_pending":
			return "Waiting for you to authorize the application on Kilocode."
		case "access_denied":
			return "Authentication was cancelled or denied."
		case "token_exchange_failed":
			return "Failed to complete authentication. Please try again."
		case "polling_timeout":
			return "Authentication timed out. Please try again."
		case "user_info_failed":
			return "Failed to get your Kilocode account information. Please try again."
		default:
			return "Authentication failed. Please try again."
		}
	}

	return "An unexpected error occurred. Please try again."
}
