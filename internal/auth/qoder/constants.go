package qoder

const (
	// qoderClientID is the default Qoder OAuth client ID for CLI tools.
	qoderClientID = "cli-proxy-api"

	// Default production (global) endpoints.
	defaultCenterURL  = "https://center.qoder.sh"
	defaultOpenapiURL = "https://openapi.qoder.sh"

	// API endpoint for chat completions.
	qoderAPIEndpoint = "https://api2-v2.qoder.sh/model/v1/chat/completions"

	// qoderVersion is the version string sent in User-Agent.
	qoderVersion = "1.0.0"

	// defaultPollInterval is the minimum interval between poll attempts.
	defaultPollInterval = 5

	// maxPollDuration is the maximum time to wait for user authorization (seconds).
	maxPollDuration = 15 * 60

	// defaultFlowExpiry is the assumed device flow expiry in seconds when not specified.
	defaultFlowExpiry = 600
)
