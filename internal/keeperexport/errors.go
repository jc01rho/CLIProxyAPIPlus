// Package keeperexport implements the strict keeper-export/v1 protocol DTOs,
// decoders, and validators defined by the frozen cross-repo contract in
// .omo/start-work/task-1-protocol-spec.md. It contains no exporter runtime:
// durable outbox, delivery, and management handlers are separate tasks.
package keeperexport

// Error is a stable keeper-export/v1 protocol error (contract section 9).
type Error struct {
	HTTPStatus      int    `json:"-"`
	Code            string `json:"code"`
	Message         string `json:"message"`
	Retryable       bool   `json:"retryable"`
	// CurrentRevision is a transport hint, parsed from the
	// X-Keeper-Export-Current-Revision response header. It is the
	// revision Keeper currently holds for this category and lets the
	// CPA exporter jump straight to revision+1 on stale/conflicting
	// responses instead of one-by-one +1 walking.
	CurrentRevision int64 `json:"-"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type errorSpec struct {
	status    int
	message   string
	retryable bool
}

// errorTable is the exact stable error set from contract section 9.
var errorTable = map[string]errorSpec{
	"invalid_json":                 {400, "request JSON is invalid", false},
	"unknown_field":                {400, "request contains an unknown field", false},
	"invalid_field":                {400, "request contains an invalid field", false},
	"body_instance_forbidden":      {400, "instance identity must not be supplied by the request body", false},
	"missing_credential":           {401, "ingest credential is required", false},
	"invalid_credential":           {401, "ingest credential is invalid", false},
	"insufficient_scope":           {403, "credential scope does not permit this operation", false},
	"instance_disabled":            {403, "instance is disabled", false},
	"instance_not_found":           {404, "instance was not found", false},
	"credential_not_found":         {404, "credential was not found", false},
	"method_not_allowed":           {405, "method is not allowed", false},
	"conflicting_replay":           {409, "sequence was previously accepted with different payload", false},
	"stale_revision":               {409, "metadata revision is older than the current revision", false},
	"conflicting_revision":         {409, "metadata revision was previously accepted with different content", false},
	"instance_state_conflict":      {409, "instance state does not permit this operation", false},
	"request_too_large":            {413, "request exceeds the maximum size", false},
	"unsupported_protocol_version": {422, "protocol version is not supported", false},
	"invalid_sequence_order":       {422, "event sequences must be strictly increasing", false},
	"batch_limit_exceeded":         {422, "usage batch exceeds an item or payload limit", false},
	"incomplete_snapshot":          {422, "metadata snapshot must be complete", false},
	"duplicate_metadata_identity":  {422, "metadata snapshot contains a duplicate identity", false},
	"invalid_settings":             {422, "usage export settings are invalid", false},
	"token_env_unset":              {422, "configured token environment variable is not set", false},
	"rate_limited":                 {429, "request rate limit exceeded", true},
	"storage_error":                {500, "durable storage operation failed", true},
	"internal_error":               {500, "internal operation failed", true},
	"keeper_unreachable":           {502, "keeper could not be reached", true},
	"keeper_invalid_response":      {502, "keeper returned an invalid response", false},
	"keeper_tls_error":             {502, "keeper TLS validation failed", false},
	"service_unavailable":          {503, "service is temporarily unavailable", true},
	"keeper_timeout":               {504, "keeper request timed out", true},
}

func protocolError(code string) *Error {
	spec, ok := errorTable[code]
	if !ok {
		spec = errorTable["internal_error"]
		code = "internal_error"
	}
	return &Error{HTTPStatus: spec.status, Code: code, Message: spec.message, Retryable: spec.retryable}
}

// HTTPStatusForCode returns the stable HTTP status for a keeper-export/v1 error
// code, or 0 when the code is not part of the contract.
func HTTPStatusForCode(code string) int {
	if spec, ok := errorTable[code]; ok {
		return spec.status
	}
	return 0
}

// IsStableCode reports whether code is part of the frozen section-9 error set.
func IsStableCode(code string) bool {
	_, ok := errorTable[code]
	return ok
}
