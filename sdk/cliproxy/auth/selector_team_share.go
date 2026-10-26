package auth

import (
	"context"
	"math"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// teamShareTieTolerance groups pool accounts whose remaining share differs by less than
// this fraction, so the wrapped selector (credential weight, round robin) still spreads
// load across near-equal accounts.
const teamShareTieTolerance = 0.02

// RemainingFunc reports the fraction (0-1) of an account's tightest rate-limit window that is
// still free. ok is false when the account is not part of a team pool.
type RemainingFunc func(authID string) (fraction float64, ok bool)

// TeamShareSelector narrows the candidates to the pool accounts with the most remaining
// usage and then lets the wrapped selector choose among them. Accounts outside every pool
// are left untouched, so credential weights keep deciding for them.
type TeamShareSelector struct {
	fallback  Selector
	remaining RemainingFunc
}

// NewTeamShareSelector wraps fallback with remaining-usage-first account preference.
func NewTeamShareSelector(fallback Selector, remaining RemainingFunc) *TeamShareSelector {
	if fallback == nil {
		fallback = &RoundRobinSelector{}
	}
	return &TeamShareSelector{fallback: fallback, remaining: remaining}
}

// Pick implements Selector.
func (s *TeamShareSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	candidates := auths
	if _, weighted := s.fallback.(*WeightedRoundRobinSelector); weighted {
		candidates = positiveWeightAuths(auths)
	}
	if available, err := getSelectorAvailableAuths(ctx, candidates, provider, model, time.Now()); err == nil {
		candidates = available
	}
	return s.fallback.Pick(ctx, provider, model, opts, s.narrow(candidates, auths))
}

func (s *TeamShareSelector) narrow(candidates, all []*Auth) []*Auth {
	if s.remaining == nil || len(candidates) < 2 {
		return all
	}
	best := -1.0
	for _, auth := range candidates {
		if fraction, ok := s.remaining(auth.ID); ok && fraction > best {
			best = fraction
		}
	}
	if best < 0 {
		return all
	}
	narrowed := make([]*Auth, 0, len(candidates))
	for _, auth := range candidates {
		fraction, ok := s.remaining(auth.ID)
		if !ok || math.Abs(fraction-best) <= teamShareTieTolerance {
			narrowed = append(narrowed, auth)
		}
	}
	return narrowed
}

// unwrapTeamShare returns the selector wrapped by a TeamShareSelector, or selector itself.
func unwrapTeamShare(selector Selector) Selector {
	if wrapper, ok := selector.(*TeamShareSelector); ok && wrapper != nil {
		return wrapper.fallback
	}
	return selector
}
