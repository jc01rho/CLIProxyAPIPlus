// Command team-member joins and removes pool members: seat, OAuth credential, API key and share.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare/lifecycle"
)

const usage = `usage:
  team-member join <name> --tier standard|premium --share <percent> [--confirm]
  team-member leave <name> [--confirm]

Without --confirm both commands only print what would change.

environment:
  CPA_MANAGEMENT_KEY       management key of the proxy (required)
  TEAM_MEMBER_CPA_URL      proxy base URL (default http://127.0.0.1:8317)
  TEAM_MEMBER_DOMAIN       member mail domain (default junghap.top)
  TEAM_MEMBER_SEAT_CONFIG  JSON file describing the claude.ai seat calls (required with --confirm)
  TEAM_MEMBER_OAUTH_CONFIG JSON file with the browser login steps and the service credential path (required for join --confirm)
  TEAM_MEMBER_AUDIT_LOG    audit log file (default ~/.cli-proxy-api/team-share-audit.log)
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func run(args []string) int {
	if len(args) < 2 || (args[0] != "join" && args[0] != "leave") {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	command, name := args[0], args[1]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	tier := fs.String("tier", "", "seat tier: standard or premium (join)")
	share := fs.Float64("share", 0, "percentage of the pool (join)")
	pool := fs.String("pool", "team", "pool name")
	confirm := fs.Bool("confirm", false, "perform the changes instead of printing the plan")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	managementKey := strings.TrimSpace(os.Getenv("CPA_MANAGEMENT_KEY"))
	if managementKey == "" {
		fmt.Fprintln(os.Stderr, "CPA_MANAGEMENT_KEY is required")
		return 2
	}
	home, _ := os.UserHomeDir()
	auditPath := envOr("TEAM_MEMBER_AUDIT_LOG", filepath.Join(home, ".cli-proxy-api", "team-share-audit.log"))
	actor := envOr("USER", "unknown")

	proxy := &lifecycle.HTTPProxy{BaseURL: envOr("TEAM_MEMBER_CPA_URL", "http://127.0.0.1:8317"), ManagementKey: managementKey}
	o := &lifecycle.Orchestrator{
		Pool: *pool, Actor: actor, Confirm: *confirm,
		Email: lifecycle.DomainEmail{Domain: envOr("TEAM_MEMBER_DOMAIN", "junghap.top")},
		Proxy: proxy,
		Audit: func(e teamshare.AuditEntry) {
			if err := teamshare.AppendAudit(auditPath, e); err != nil {
				fmt.Fprintf(os.Stderr, "warning: audit log write failed: %v\n", err)
			}
		},
	}
	o.Seats = unconfiguredSeats{}
	o.OAuth = unconfiguredOAuth{}
	if *confirm {
		seats, err := seatManagerFromEnv()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		o.Seats = seats
		if command == "join" {
			registrar, err := oauthRegistrarFromEnv(proxy)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			o.OAuth = registrar
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var (
		report lifecycle.Report
		err    error
	)
	if command == "join" {
		report, err = o.Join(ctx, name, *tier, *share)
	} else {
		report, err = o.Leave(ctx, name)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

type unconfiguredSeats struct{}

func (unconfiguredSeats) Assign(context.Context, string, string) error {
	return fmt.Errorf("seat manager is not configured")
}
func (unconfiguredSeats) Remove(context.Context, string) error {
	return fmt.Errorf("seat manager is not configured")
}

type unconfiguredOAuth struct{}

func (unconfiguredOAuth) Register(context.Context, string) (string, error) {
	return "", fmt.Errorf("oauth registrar is not configured")
}

func seatManagerFromEnv() (lifecycle.SeatManager, error) {
	path := strings.TrimSpace(os.Getenv("TEAM_MEMBER_SEAT_CONFIG"))
	if path == "" {
		return nil, fmt.Errorf("TEAM_MEMBER_SEAT_CONFIG is required with --confirm")
	}
	endpoints, err := lifecycle.LoadSeatEndpoints(path)
	if err != nil {
		return nil, fmt.Errorf("seat config: %w", err)
	}
	return &lifecycle.HTTPSeatManager{Endpoints: endpoints}, nil
}
