package qoder

import (
	"errors"
	"fmt"
	"net/http"
)

// OAuthError represents an OAuth-specific error.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	StatusCode  int    `json:"-"`
}

// Error returns a string representation of the OAuth error.
func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("OAuth error %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("OAuth error: %s", e.Code)
}

// NewOAuthError creates a new OAuth error.
func NewOAuthError(code, description string, statusCode int) *OAuthError {
	return &OAuthError{Code: code, Description: description, StatusCode: statusCode}
}

// AuthenticationError represents authentication-related errors.
type AuthenticationError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    int    `json:"code"`
	Cause   error  `json:"-"`
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

// Common authentication error types for Qoder device flow.
var (
	ErrDeviceFlowFailed = &AuthenticationError{
		Type:    "device_flow_failed",
		Message: "Failed to start Qoder device flow",
		Code:    http.StatusBadRequest,
	}
	ErrPollingTimeout = &AuthenticationError{
		Type:    "polling_timeout",
		Message: "Timeout waiting for user authorization",
		Code:    http.StatusRequestTimeout,
	}
	ErrAccessDenied = &AuthenticationError{
		Type:    "access_denied",
		Message: "User denied authorization",
		Code:    http.StatusForbidden,
	}
	ErrTokenExchangeFailed = &AuthenticationError{
		Type:    "token_exchange_failed",
		Message: "Failed to exchange device code for access token",
		Code:    http.StatusBadRequest,
	}
	ErrRefreshFailed = &AuthenticationError{
		Type:    "refresh_failed",
		Message: "Failed to refresh access token",
		Code:    http.StatusUnauthorized,
	}
	ErrPATExchangeFailed = &AuthenticationError{
		Type:    "pat_exchange_failed",
		Message: "Failed to exchange personal access token",
		Code:    http.StatusBadRequest,
	}
)

// NewAuthenticationError creates a new authentication error with a cause.
func NewAuthenticationError(baseErr *AuthenticationError, cause error) *AuthenticationError {
	return &AuthenticationError{
		Type:    baseErr.Type,
		Message: baseErr.Message,
		Code:    baseErr.Code,
		Cause:   cause,
	}
}

// IsAuthenticationError checks if an error is an AuthenticationError.
func IsAuthenticationError(err error) bool {
	var authErr *AuthenticationError
	return errors.As(err, &authErr)
}

// GetUserFriendlyMessage returns a user-friendly error message.
func GetUserFriendlyMessage(err error) string {
	var authErr *AuthenticationError
	if errors.As(err, &authErr) {
		switch authErr.Type {
		case "device_flow_failed":
			return "Failed to start Qoder authentication. Please check your network connection and try again."
		case "polling_timeout":
			return "Authentication timed out. Please try again."
		case "access_denied":
			return "Authentication was cancelled or denied."
		case "token_exchange_failed":
			return "Failed to complete authentication. Please try again."
		case "refresh_failed":
			return "Failed to refresh your Qoder session. Please log in again."
		case "pat_exchange_failed":
			if authErr.Cause != nil {
				return fmt.Sprintf("Failed to exchange personal access token: %v", authErr.Cause)
			}
			return "Failed to exchange personal access token. Please check the token and try again."
		default:
			return "Authentication failed. Please try again."
		}
	}
	return "An unexpected error occurred. Please try again."
}
