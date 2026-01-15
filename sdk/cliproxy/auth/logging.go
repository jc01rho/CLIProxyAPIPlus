package auth

import (
	"time"

	log "github.com/sirupsen/logrus"
)

// Structured field keys for auth logging
const (
	FieldAuthID        = "auth_id"
	FieldProvider      = "provider"
	FieldModel         = "model"
	FieldReason        = "reason"
	FieldDuration      = "duration"
	FieldHTTPStatus    = "http_status"
	FieldFromProvider  = "from_provider"
	FieldToProvider    = "to_provider"
	FieldFromModel     = "from_model"
	FieldToModel       = "to_model"
	FieldCooldownCount = "cooldown_count"
	FieldEarliestReset = "earliest_reset"
)

// logKeySelected logs when a key is selected for use
func logKeySelected(authID, provider, model, reason string) {
	log.WithFields(log.Fields{
		FieldAuthID:   authID,
		FieldProvider: provider,
		FieldModel:    model,
		FieldReason:   reason,
	}).Debug("Key selected")
}

// logKeyBlocked logs when a key is marked as blocked/unavailable
func logKeyBlocked(authID, provider, model, reason string, httpStatus int, duration time.Duration) {
	log.WithFields(log.Fields{
		FieldAuthID:     authID,
		FieldProvider:   provider,
		FieldModel:      model,
		FieldReason:     reason,
		FieldHTTPStatus: httpStatus,
		FieldDuration:   duration.String(),
	}).Debug("Key blocked")
}

// logKeyRecovered logs when a previously blocked key becomes available
func logKeyRecovered(authID, provider, model string) {
	log.WithFields(log.Fields{
		FieldAuthID:   authID,
		FieldProvider: provider,
		FieldModel:    model,
	}).Debug("Key recovered")
}

// logFallbackTriggered logs when fallback occurs between models
func logFallbackTriggered(fromModel, toModel, reason string) {
	log.WithFields(log.Fields{
		FieldFromModel: fromModel,
		FieldToModel:   toModel,
		FieldReason:    reason,
	}).Debug("Model fallback triggered")
}

// logProviderFallback logs when fallback occurs between providers
func logProviderFallback(fromProvider, toProvider, model, reason string) {
	log.WithFields(log.Fields{
		FieldFromProvider: fromProvider,
		FieldToProvider:   toProvider,
		FieldModel:        model,
		FieldReason:       reason,
	}).Debug("Provider fallback triggered")
}

// logCooldownWait logs when entering cooldown wait period
func logCooldownWait(provider, model string, duration time.Duration) {
	log.WithFields(log.Fields{
		FieldProvider: provider,
		FieldModel:    model,
		FieldDuration: duration.String(),
	}).Debug("Waiting for cooldown")
}

// logAllKeysExhausted logs when no keys are available for a model
func logAllKeysExhausted(provider, model string, cooldownCount int, earliestReset time.Duration) {
	log.WithFields(log.Fields{
		FieldProvider:      provider,
		FieldModel:         model,
		FieldCooldownCount: cooldownCount,
		FieldEarliestReset: earliestReset.String(),
	}).Debug("All keys exhausted for model")
}
