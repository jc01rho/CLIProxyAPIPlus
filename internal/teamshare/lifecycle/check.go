package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

// CheckReport is the outcome of a post-seat-change credential check.
type CheckReport struct {
	Email          string                 `json:"email"`
	AuthFile       string                 `json:"auth_file"`
	RefreshOK      bool                   `json:"refresh_ok"`
	RefreshMessage string                 `json:"refresh_message,omitempty"`
	Account        *teamshare.AccountView `json:"account,omitempty"`
}

// RefreshAuthFile forces a token refresh of one stored credential and returns the raw result.
func (p *HTTPProxy) RefreshAuthFile(ctx context.Context, name string) (ok bool, message string, err error) {
	var out struct {
		Results []struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"results"`
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err = p.do(ctx, http.MethodPost, "/auth-files/refresh", map[string]string{"name": name}, &out); err != nil {
		return false, err.Error(), nil
	}
	for _, r := range out.Results {
		if !r.Success {
			return false, r.Error, nil
		}
	}
	if out.Error != "" {
		return false, out.Error, nil
	}
	raw, _ := json.Marshal(out)
	return true, string(raw), nil
}

// Check refreshes the credential of email with its stored refresh token and reports how the
// pool sees the account afterwards. Run it after a seat was reassigned or its tier changed:
// a working refresh means no re-registration is needed, and calibration then follows the
// utilization signals of the new seat.
func Check(ctx context.Context, proxy *HTTPProxy, pool, email string) (CheckReport, error) {
	rep := CheckReport{Email: email}
	file, err := proxy.FindAuthFile(ctx, email)
	if err != nil {
		return rep, err
	}
	if file == "" {
		return rep, fmt.Errorf("no stored credential for %s", email)
	}
	rep.AuthFile = file
	rep.RefreshOK, rep.RefreshMessage, err = proxy.RefreshAuthFile(ctx, file)
	if err != nil {
		return rep, err
	}
	snap, err := proxy.Snapshot(ctx)
	if err != nil {
		return rep, err
	}
	if p, ok := poolView(snap, pool); ok {
		for i := range p.Accounts {
			if strings.EqualFold(p.Accounts[i].Ref, email) || strings.EqualFold(p.Accounts[i].Email, email) {
				rep.Account = &p.Accounts[i]
			}
		}
	}
	return rep, nil
}
