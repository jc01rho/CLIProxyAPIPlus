package access

import (
	"fmt"
	"net/http"
	"strings"
)

// AuthErrorCode classifies authentication failures.
type AuthErrorCode string

const (
	AuthErrorCodeNoCredentials     AuthErrorCode = "no_credentials"
	AuthErrorCodeInvalidCredential AuthErrorCode = "invalid_credential"
	AuthErrorCodeNotHandled        AuthErrorCode = "not_handled"
	AuthErrorCodeDenied            AuthErrorCode = "denied"
	AuthErrorCodeRequestTooLarge   AuthErrorCode = "request_too_large"
	AuthErrorCodeInternal          AuthErrorCode = "internal_error"
)

// AuthError carries authentication failure details and HTTP status.
type AuthError struct {
	Code         AuthErrorCode
	Message      string
	StatusCode   int
	Cause        error
	ProviderType string
}

func (e *AuthError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "authentication error"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", message, e.Cause)
	}
	return message
}

func (e *AuthError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// HTTPStatusCode returns a safe fallback for missing status codes.
func (e *AuthError) HTTPStatusCode() int {
	if e == nil || e.StatusCode <= 0 {
		if e != nil && e.Code == AuthErrorCodeDenied {
			return http.StatusForbidden
		}
		return http.StatusInternalServerError
	}
	return e.StatusCode
}

func newAuthError(code AuthErrorCode, message string, statusCode int, cause error) *AuthError {
	return &AuthError{
		Code:       code,
		Message:    message,
		StatusCode: statusCode,
		Cause:      cause,
	}
}

func newProviderAuthError(code AuthErrorCode, message string, statusCode int, cause error, providerType string) *AuthError {
	authErr := newAuthError(code, message, statusCode, cause)
	authErr.ProviderType = strings.TrimSpace(providerType)
	return authErr
}

func NewNoCredentialsError() *AuthError {
	return newAuthError(AuthErrorCodeNoCredentials, "Missing API key", http.StatusUnauthorized, nil)
}

func NewInvalidCredentialError() *AuthError {
	return newAuthError(AuthErrorCodeInvalidCredential, "Invalid API key", http.StatusUnauthorized, nil)
}

func NewInvalidCredentialErrorForProvider(providerType string) *AuthError {
	return newProviderAuthError(AuthErrorCodeInvalidCredential, "Invalid API key", http.StatusUnauthorized, nil, providerType)
}

func NewNotHandledError() *AuthError {
	return newAuthError(AuthErrorCodeNotHandled, "authentication provider did not handle request", 0, nil)
}

func NewDeniedError(message string, statusCode int) *AuthError {
	if statusCode <= 0 {
		statusCode = http.StatusForbidden
	}
	normalizedMessage := strings.TrimSpace(message)
	if normalizedMessage == "" {
		normalizedMessage = http.StatusText(statusCode)
		if normalizedMessage == "" {
			normalizedMessage = "Access denied"
		}
	}
	return newAuthError(AuthErrorCodeDenied, normalizedMessage, statusCode, nil)
}

// NewRequestEntityTooLargeError builds a 413 error when the request body exceeds the configured limit.
func NewRequestEntityTooLargeError(cause error) *AuthError {
	return newAuthError(AuthErrorCodeRequestTooLarge, "http: request body too large", http.StatusRequestEntityTooLarge, cause)
}

func NewInternalAuthError(message string, cause error) *AuthError {
	normalizedMessage := strings.TrimSpace(message)
	if normalizedMessage == "" {
		normalizedMessage = "Authentication service error"
	}
	return newAuthError(AuthErrorCodeInternal, normalizedMessage, http.StatusInternalServerError, cause)
}

func IsAuthErrorCode(authErr *AuthError, code AuthErrorCode) bool {
	if authErr == nil {
		return false
	}
	return authErr.Code == code
}
