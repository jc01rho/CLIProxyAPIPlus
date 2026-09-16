package workbuddy

import "errors"

var (
	ErrPollingTimeout   = errors.New("workbuddy: polling timeout, user did not authorize in time")
	ErrAccessDenied     = errors.New("workbuddy: access denied by user")
	ErrTokenFetchFailed = errors.New("workbuddy: failed to fetch token from server")
	ErrJWTDecodeFailed  = errors.New("workbuddy: failed to decode JWT token")
	ErrAccountLookup    = errors.New("workbuddy: failed to fetch account identity")
)

// GetUserFriendlyMessage converts authentication errors into actionable login messages.
func GetUserFriendlyMessage(err error) string {
	switch {
	case errors.Is(err, ErrPollingTimeout):
		return "Authentication timed out. Please try again."
	case errors.Is(err, ErrAccessDenied):
		return "Access denied. Please try again and approve the login request."
	case errors.Is(err, ErrJWTDecodeFailed):
		return "Failed to decode token. Please try logging in again."
	case errors.Is(err, ErrTokenFetchFailed):
		return "Failed to fetch token from server. Please try again."
	case errors.Is(err, ErrAccountLookup):
		return "Failed to fetch account identity. Please try logging in again."
	default:
		return "Authentication failed: " + err.Error()
	}
}
