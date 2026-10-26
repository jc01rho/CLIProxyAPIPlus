// Package lifecycle runs the join and leave workflows of a team member: email address,
// claude.ai seat, OAuth credential, client API key and pool share, with a dry-run default
// for every step that bills or cannot be undone.
package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

// EmailProvisioner makes sure the member's address at the team domain can receive mail.
type EmailProvisioner interface {
	Ensure(ctx context.Context, name string) (email string, err error)
}

// SeatManager assigns and releases claude.ai organization seats.
type SeatManager interface {
	Assign(ctx context.Context, email, tier string) error
	Remove(ctx context.Context, email string) error
}

// OAuthRegistrar signs the seat account in to Claude and stores the credential in the proxy.
type OAuthRegistrar interface {
	Register(ctx context.Context, email string) (authFile string, err error)
}

// Proxy is the management surface of the CLIProxyAPIPlus instance.
type Proxy interface {
	AddAPIKey(ctx context.Context, key string) error
	RemoveAPIKeyByKeyID(ctx context.Context, keyID string) error
	PutPoolAccount(ctx context.Context, pool, auth, tier string) error
	RemovePoolAccount(ctx context.Context, pool, auth string) error
	PutMember(ctx context.Context, pool, name, apiKey string, percent float64) error
	RemoveMember(ctx context.Context, pool, name string) error
	FindAuthFile(ctx context.Context, email string) (name string, err error)
	DeleteAuthFile(ctx context.Context, name string) error
	Snapshot(ctx context.Context) (teamshare.Snapshot, error)
}

// Action is one planned step.
type Action struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Irreversible bool   `json:"irreversible,omitempty"`
	Done         bool   `json:"done"`
	Error        string `json:"error,omitempty"`
}

// Report describes what a workflow would do or did.
type Report struct {
	Operation string   `json:"operation"`
	Name      string   `json:"name"`
	Email     string   `json:"email,omitempty"`
	Tier      string   `json:"tier,omitempty"`
	Share     float64  `json:"share_percent,omitempty"`
	DryRun    bool     `json:"dry_run"`
	Actions   []Action `json:"actions"`
	// APIKey is set once, on a confirmed join, and never written to the audit log.
	APIKey     string  `json:"api_key,omitempty"`
	PoolBefore float64 `json:"pool_five_hour_usd_before"`
	PoolAfter  float64 `json:"pool_five_hour_usd_after,omitempty"`
}

// Orchestrator wires the collaborators of the two workflows.
type Orchestrator struct {
	Pool    string
	Actor   string
	Confirm bool
	Email   EmailProvisioner
	Seats   SeatManager
	OAuth   OAuthRegistrar
	Proxy   Proxy
	Audit   func(teamshare.AuditEntry)
	Now     func() time.Time
}

var namePattern = regexp.MustCompile("^[a-z0-9][a-z0-9-]{1,30}$")

func (o *Orchestrator) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o *Orchestrator) audit(action, name, tier, seat, result string, detail map[string]string) {
	if o.Audit == nil {
		return
	}
	o.Audit(teamshare.AuditEntry{Time: o.now(), Actor: o.Actor, Action: action, Name: name, Tier: tier, Seat: seat, Result: result, Detail: detail})
}

func newAPIKey(name string) (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "sk-team-" + name + "-" + hex.EncodeToString(raw[:]), nil
}

func poolView(snap teamshare.Snapshot, pool string) (teamshare.PoolView, bool) {
	for _, p := range snap.Pools {
		if strings.EqualFold(p.Name, pool) {
			return p, true
		}
	}
	return teamshare.PoolView{}, false
}

func assignedPercent(p teamshare.PoolView) float64 {
	sum := 0.0
	for _, m := range p.Members {
		sum += m.Percent
	}
	return sum
}

// Join creates the member end to end. Without Confirm it only reports the plan.
func (o *Orchestrator) Join(ctx context.Context, name, tier string, share float64) (Report, error) {
	rep := Report{Operation: "join", Name: name, Tier: tier, Share: share, DryRun: !o.Confirm}
	if !namePattern.MatchString(name) {
		return rep, fmt.Errorf("invalid member name %q: use 2-31 lowercase letters, digits or hyphens", name)
	}
	if tier != "standard" && tier != "premium" {
		return rep, fmt.Errorf("tier must be standard or premium, got %q", tier)
	}
	if share <= 0 || share > 100 {
		return rep, fmt.Errorf("share must be within (0,100], got %v", share)
	}
	snap, err := o.Proxy.Snapshot(ctx)
	if err != nil {
		return rep, fmt.Errorf("read pool state: %w", err)
	}
	pool, found := poolView(snap, o.Pool)
	if found {
		rep.PoolBefore = pool.FiveHour.TotalUSD
		for _, m := range pool.Members {
			if strings.EqualFold(m.Name, name) {
				return rep, fmt.Errorf("member %q already exists in pool %q", name, o.Pool)
			}
		}
		if free := 100 - assignedPercent(pool); share > free+1e-9 {
			return rep, fmt.Errorf("share %.2f%% exceeds the unassigned %.2f%% of pool %q", share, free, o.Pool)
		}
	}
	email, err := o.Email.Ensure(ctx, name)
	if err != nil {
		return rep, fmt.Errorf("email address: %w", err)
	}
	rep.Email = email
	rep.Actions = []Action{
		{Name: "email", Description: "confirm " + email + " is routable"},
		{Name: "seat", Description: fmt.Sprintf("assign a %s seat to %s (billable)", tier, email), Irreversible: true},
		{Name: "oauth", Description: "sign " + email + " in through the SSO passkey and store the Claude credential"},
		{Name: "api-key", Description: "issue a client API key for " + name},
		{Name: "pool", Description: fmt.Sprintf("add %s to pool %q as %s and give %s %.2f%%", email, o.Pool, tier, name, share)},
		{Name: "verify", Description: "read the pool total back"},
	}
	if rep.DryRun {
		o.audit("join-dry-run", name, tier, email, "planned", nil)
		return rep, nil
	}

	step := func(i int, fn func() error) error {
		if err := fn(); err != nil {
			rep.Actions[i].Error = err.Error()
			o.audit("join-"+rep.Actions[i].Name, name, tier, email, "failed", map[string]string{"error": err.Error()})
			return fmt.Errorf("%s: %w", rep.Actions[i].Name, err)
		}
		rep.Actions[i].Done = true
		o.audit("join-"+rep.Actions[i].Name, name, tier, email, "ok", nil)
		return nil
	}
	var authFile, apiKey string
	steps := []func() error{
		func() error { return nil },
		func() error { return o.Seats.Assign(ctx, email, tier) },
		func() (err error) { authFile, err = o.OAuth.Register(ctx, email); return },
		func() error {
			var err error
			if apiKey, err = newAPIKey(name); err != nil {
				return err
			}
			return o.Proxy.AddAPIKey(ctx, apiKey)
		},
		func() error {
			if err := o.Proxy.PutPoolAccount(ctx, o.Pool, email, tier); err != nil {
				return err
			}
			return o.Proxy.PutMember(ctx, o.Pool, name, apiKey, share)
		},
		func() error {
			after, err := o.Proxy.Snapshot(ctx)
			if err != nil {
				return err
			}
			p, ok := poolView(after, o.Pool)
			if !ok {
				return errors.New("pool missing after join")
			}
			rep.PoolAfter = p.FiveHour.TotalUSD
			return nil
		},
	}
	for i, fn := range steps {
		if err := step(i, fn); err != nil {
			return rep, err
		}
	}
	rep.APIKey = apiKey
	o.audit("join", name, tier, email, "ok", map[string]string{"auth_file": authFile, "share_percent": fmt.Sprintf("%.2f", share)})
	return rep, nil
}

// Leave removes the member. Usage stops first and the seat is released last, so a failure
// part-way never leaves a working key attached to a released seat. Without Confirm it only
// reports the plan.
func (o *Orchestrator) Leave(ctx context.Context, name string) (Report, error) {
	rep := Report{Operation: "leave", Name: name, DryRun: !o.Confirm}
	snap, err := o.Proxy.Snapshot(ctx)
	if err != nil {
		return rep, fmt.Errorf("read pool state: %w", err)
	}
	pool, found := poolView(snap, o.Pool)
	if !found {
		return rep, fmt.Errorf("pool %q not found", o.Pool)
	}
	rep.PoolBefore = pool.FiveHour.TotalUSD
	var member *teamshare.MemberView
	for i := range pool.Members {
		if strings.EqualFold(pool.Members[i].Name, name) {
			member = &pool.Members[i]
		}
	}
	if member == nil {
		return rep, fmt.Errorf("member %q not found in pool %q", name, o.Pool)
	}
	rep.Share = member.Percent
	email, err := o.Email.Ensure(ctx, name)
	if err != nil {
		return rep, fmt.Errorf("email address: %w", err)
	}
	rep.Email = email
	tier := "standard"
	for _, a := range pool.Accounts {
		if strings.EqualFold(a.Ref, email) {
			tier = a.Tier
		}
	}
	rep.Tier = tier
	rep.Actions = []Action{
		{Name: "revoke-key", Description: fmt.Sprintf("revoke API key %s and drop %s from pool %q (frees %.2f%%, left unassigned)", member.KeyID, name, o.Pool, member.Percent), Irreversible: true},
		{Name: "auth-file", Description: "disable and delete the stored Claude credential of " + email, Irreversible: true},
		{Name: "pool-account", Description: "remove " + email + " from the pool total"},
		{Name: "seat", Description: "remove " + email + " from the claude.ai organization and return the seat", Irreversible: true},
		{Name: "verify", Description: "read the pool total back"},
	}
	if rep.DryRun {
		o.audit("leave-dry-run", name, tier, email, "planned", nil)
		return rep, nil
	}

	step := func(i int, fn func() error) error {
		if err := fn(); err != nil {
			rep.Actions[i].Error = err.Error()
			o.audit("leave-"+rep.Actions[i].Name, name, tier, email, "failed", map[string]string{"error": err.Error()})
			return fmt.Errorf("%s: %w", rep.Actions[i].Name, err)
		}
		rep.Actions[i].Done = true
		o.audit("leave-"+rep.Actions[i].Name, name, tier, email, "ok", nil)
		return nil
	}
	steps := []func() error{
		func() error {
			if err := o.Proxy.RemoveAPIKeyByKeyID(ctx, member.KeyID); err != nil {
				return err
			}
			return o.Proxy.RemoveMember(ctx, o.Pool, name)
		},
		func() error {
			file, err := o.Proxy.FindAuthFile(ctx, email)
			if err != nil {
				return err
			}
			if file == "" {
				return nil
			}
			return o.Proxy.DeleteAuthFile(ctx, file)
		},
		func() error { return o.Proxy.RemovePoolAccount(ctx, o.Pool, email) },
		func() error { return o.Seats.Remove(ctx, email) },
		func() error {
			after, err := o.Proxy.Snapshot(ctx)
			if err != nil {
				return err
			}
			if p, ok := poolView(after, o.Pool); ok {
				rep.PoolAfter = p.FiveHour.TotalUSD
			}
			return nil
		},
	}
	for i, fn := range steps {
		if err := step(i, fn); err != nil {
			return rep, err
		}
	}
	o.audit("leave", name, tier, email, "ok", map[string]string{"released_share_percent": fmt.Sprintf("%.2f", member.Percent)})
	return rep, nil
}
