package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

type world struct {
	calls   []string
	members []teamshare.MemberView
	account []teamshare.AccountView
	audit   []teamshare.AuditEntry
	seatErr error
	apiKey  string
	total   float64
}

func (w *world) record(s string) { w.calls = append(w.calls, s) }

func (w *world) Ensure(_ context.Context, name string) (string, error) {
	return name + "@junghap.top", nil
}
func (w *world) Assign(_ context.Context, email, tier string) error {
	w.record("seat-assign " + email + " " + tier)
	return w.seatErr
}
func (w *world) Remove(_ context.Context, email string) error {
	w.record("seat-remove " + email)
	return w.seatErr
}
func (w *world) Register(_ context.Context, email string) (string, error) {
	w.record("oauth " + email)
	return email + ".json", nil
}
func (w *world) AddAPIKey(_ context.Context, key string) error {
	w.apiKey = key
	w.record("key-add")
	return nil
}
func (w *world) RemoveAPIKeyByKeyID(_ context.Context, id string) error {
	w.record("key-revoke " + id)
	return nil
}
func (w *world) PutPoolAccount(_ context.Context, _, auth, tier string) error {
	w.record("pool-account " + auth + " " + tier)
	w.account = append(w.account, teamshare.AccountView{Ref: auth, Tier: tier})
	w.total += map[string]float64{"standard": 6, "premium": 30}[tier]
	return nil
}
func (w *world) RemovePoolAccount(_ context.Context, _, auth string) error {
	w.record("pool-account-remove " + auth)
	kept := w.account[:0:0]
	for _, a := range w.account {
		if a.Ref == auth {
			w.total -= map[string]float64{"standard": 6, "premium": 30}[a.Tier]
			continue
		}
		kept = append(kept, a)
	}
	w.account = kept
	return nil
}
func (w *world) PutMember(_ context.Context, _, name, key string, pct float64) error {
	w.record("member-put " + name)
	w.members = append(w.members, teamshare.MemberView{Name: name, KeyID: teamshare.KeyID(key), Percent: pct})
	return nil
}
func (w *world) RemoveMember(_ context.Context, _, name string) error {
	w.record("member-remove " + name)
	kept := w.members[:0:0]
	for _, m := range w.members {
		if m.Name != name {
			kept = append(kept, m)
		}
	}
	w.members = kept
	return nil
}
func (w *world) FindAuthFile(_ context.Context, email string) (string, error) {
	return email + ".json", nil
}
func (w *world) DeleteAuthFile(_ context.Context, name string) error {
	w.record("auth-delete " + name)
	return nil
}
func (w *world) Snapshot(_ context.Context) (teamshare.Snapshot, error) {
	return teamshare.Snapshot{Pools: []teamshare.PoolView{{
		Name:     "team",
		FiveHour: teamshare.PoolWindowView{TotalUSD: w.total},
		Accounts: w.account,
		Members:  w.members,
	}}}, nil
}

func newOrchestrator(w *world, confirm bool) *Orchestrator {
	return &Orchestrator{
		Pool: "team", Actor: "tester", Confirm: confirm,
		Email: w, Seats: w, OAuth: w, Proxy: w,
		Audit: func(e teamshare.AuditEntry) { w.audit = append(w.audit, e) },
		Now:   func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) },
	}
}

func TestJoinWithoutConfirmOnlyPlans(t *testing.T) {
	w := &world{}
	rep, err := newOrchestrator(w, false).Join(context.Background(), "dana", "standard", 25)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || len(rep.Actions) != 6 || len(w.calls) != 0 {
		t.Fatalf("dry run must not change anything: dry=%v actions=%d calls=%v", rep.DryRun, len(rep.Actions), w.calls)
	}
	if rep.Email != "dana@junghap.top" {
		t.Fatalf("email = %q", rep.Email)
	}
}

func TestJoinRunsEveryStepInOrderAndAuditsWithoutTheKey(t *testing.T) {
	w := &world{}
	rep, err := newOrchestrator(w, true).Join(context.Background(), "dana", "premium", 25)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"seat-assign dana@junghap.top premium", "oauth dana@junghap.top", "key-add", "pool-account dana@junghap.top premium", "member-put dana"}
	if strings.Join(w.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", w.calls)
	}
	if rep.DryRun || rep.APIKey == "" || rep.APIKey != w.apiKey || rep.PoolAfter != 30 {
		t.Fatalf("report = %+v", rep)
	}
	for _, e := range w.audit {
		if strings.Contains(e.Result+e.Action+e.Name+e.Seat, rep.APIKey) {
			t.Fatal("the API key must never reach the audit log")
		}
		for _, v := range e.Detail {
			if strings.Contains(v, rep.APIKey) {
				t.Fatal("the API key must never reach the audit log")
			}
		}
	}
	last := w.audit[len(w.audit)-1]
	if last.Action != "join" || last.Result != "ok" || last.Actor != "tester" || last.Tier != "premium" || last.Seat != "dana@junghap.top" || last.Time.IsZero() {
		t.Fatalf("final audit entry = %+v", last)
	}
}

func TestJoinRejectsShareBeyondTheUnassignedRemainder(t *testing.T) {
	w := &world{members: []teamshare.MemberView{{Name: "alice", Percent: 60}, {Name: "bob", Percent: 30}}}
	_, err := newOrchestrator(w, true).Join(context.Background(), "dana", "standard", 20)
	if err == nil || !strings.Contains(err.Error(), "exceeds the unassigned") {
		t.Fatalf("err = %v", err)
	}
	if len(w.calls) != 0 {
		t.Fatalf("nothing may run before the share check: %v", w.calls)
	}
}

func TestJoinStopsAtTheFailingStepAndAuditsIt(t *testing.T) {
	w := &world{seatErr: errors.New("seat limit reached")}
	rep, err := newOrchestrator(w, true).Join(context.Background(), "dana", "standard", 10)
	if err == nil || !strings.Contains(err.Error(), "seat") {
		t.Fatalf("err = %v", err)
	}
	if len(w.calls) != 1 || rep.APIKey != "" {
		t.Fatalf("later steps must not run after a failed seat: calls=%v key=%q", w.calls, rep.APIKey)
	}
	if got := w.audit[len(w.audit)-1]; got.Action != "join-seat" || got.Result != "failed" {
		t.Fatalf("audit = %+v", got)
	}
}

func TestLeaveWithoutConfirmReportsPlanAndChangesNothing(t *testing.T) {
	w := &world{members: []teamshare.MemberView{{Name: "bob", KeyID: "k-bob", Percent: 40}}, total: 12}
	rep, err := newOrchestrator(w, false).Leave(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || len(w.calls) != 0 || len(rep.Actions) != 5 {
		t.Fatalf("dry run changed state: %+v calls=%v", rep, w.calls)
	}
	irreversible := 0
	for _, a := range rep.Actions {
		if a.Irreversible {
			irreversible++
		}
	}
	if irreversible != 3 {
		t.Fatalf("irreversible actions flagged = %d", irreversible)
	}
}

func TestLeaveRevokesUsageFirstAndReleasesTheSeatLast(t *testing.T) {
	w := &world{
		members: []teamshare.MemberView{{Name: "alice", KeyID: "k-alice", Percent: 60}, {Name: "bob", KeyID: "k-bob", Percent: 40}},
		account: []teamshare.AccountView{{Ref: "bob@junghap.top", Tier: "standard"}, {Ref: "alice@junghap.top", Tier: "standard"}},
		total:   12,
	}
	rep, err := newOrchestrator(w, true).Leave(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"key-revoke k-bob", "member-remove bob", "auth-delete bob@junghap.top.json", "pool-account-remove bob@junghap.top", "seat-remove bob@junghap.top"}
	if strings.Join(w.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", w.calls)
	}
	if rep.PoolBefore != 12 || rep.PoolAfter != 6 {
		t.Fatalf("pool total before/after = %v/%v", rep.PoolBefore, rep.PoolAfter)
	}
	if len(w.members) != 1 || w.members[0].Percent != 60 {
		t.Fatalf("remaining members must keep their percentage, freed share stays unassigned: %+v", w.members)
	}
	if got := w.audit[len(w.audit)-1]; got.Action != "leave" || got.Name != "bob" {
		t.Fatalf("audit = %+v", got)
	}
}

func TestLeaveKeepsTheSeatWhenAnEarlierStepFails(t *testing.T) {
	w := &world{members: []teamshare.MemberView{{Name: "bob", KeyID: "k-bob", Percent: 40}}, total: 6, seatErr: errors.New("boom")}
	_, err := newOrchestrator(w, true).Leave(context.Background(), "bob")
	if err == nil {
		t.Fatal("seat removal failure must surface")
	}
	if w.calls[0] != "key-revoke k-bob" {
		t.Fatalf("usage must stop before the seat is touched: %v", w.calls)
	}
	if got := w.audit[len(w.audit)-1]; got.Action != "leave-seat" || got.Result != "failed" {
		t.Fatalf("audit = %+v", got)
	}
}

type lagProxy struct {
	*world
	calls int
	stale teamshare.Snapshot
}

func (l *lagProxy) Snapshot(ctx context.Context) (teamshare.Snapshot, error) {
	l.calls++
	fresh, err := l.world.Snapshot(ctx)
	if l.calls == 1 {
		l.stale = fresh
	}
	if l.calls > 1 && l.calls <= 3 {
		return l.stale, err
	}
	return fresh, err
}

func TestLeaveVerifyWaitsForTheProxyToApplyTheSavedConfig(t *testing.T) {
	w := &world{
		members: []teamshare.MemberView{{Name: "alice", KeyID: "k-alice", Percent: 60}, {Name: "bob", KeyID: "k-bob", Percent: 40}},
		account: []teamshare.AccountView{{Ref: "bob@junghap.top", Tier: "standard"}, {Ref: "alice@junghap.top", Tier: "standard"}},
		total:   12,
	}
	lag := &lagProxy{world: w}
	o := newOrchestrator(w, true)
	o.Proxy = lag
	rep, err := o.Leave(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if rep.PoolAfter != 6 || lag.calls < 4 {
		t.Fatalf("PoolAfter=%v after %d snapshots: verify must wait for the applied state", rep.PoolAfter, lag.calls)
	}
}
