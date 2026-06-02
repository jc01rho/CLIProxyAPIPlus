package qoder

const (
	// qoderClientID is the default Qoder OAuth client ID for CLI tools.
	qoderClientID = "cli-proxy-api"

	// Default production (global) endpoints.
	defaultCenterURL  = "https://center.qoder.sh"
	defaultOpenapiURL = "https://openapi.qoder.sh"

	// BaseURL is the production API base (the host serving the chat
	// completions endpoint). It is exported so the executor can build a
	// full request URL without depending on an unexported constant.
	BaseURL = "https://api2-v2.qoder.sh"

	// ChatCompletionsPath is appended to BaseURL to form the chat
	// completions request URL. The path mirrors the @ali/qoder-agent-sdk.
	ChatCompletionsPath = "/model/v1/chat/completions"

	// UserAgent is sent on every Qoder API request. The version suffix is
	// kept in lockstep with the Qoder CLI releases.
	UserAgent = "qoder/1.0.0"

	// qoderVersion is the version string sent in User-Agent.
	qoderVersion = "1.0.0"

	// defaultPollInterval is the minimum interval between poll attempts.
	defaultPollInterval = 5

	// maxPollDuration is the maximum time to wait for user authorization (seconds).
	maxPollDuration = 15 * 60

	// defaultFlowExpiry is the assumed device flow expiry in seconds when not specified.
	defaultFlowExpiry = 600
)
