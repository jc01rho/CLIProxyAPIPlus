package management

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

var (
	errTeamSharePoolNotFound    = errors.New("pool not found")
	errTeamShareMemberNotFound  = errors.New("member not found")
	errTeamShareAccountNotFound = errors.New("account not found")
)

// GetTeamShare returns per-pool totals, per-account utilization and per-member allocation.
func (h *Handler) GetTeamShare(c *gin.Context) {
	c.JSON(http.StatusOK, teamshare.Default().Snapshot())
}

// PostTeamShareAckReauth clears a re-authentication flag after the operator re-registered
// the credential. Re-login itself is never automatic.
func (h *Handler) PostTeamShareAckReauth(c *gin.Context) {
	authID := strings.TrimSpace(c.Query("auth_id"))
	if authID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_id is required"})
		return
	}
	if !teamshare.Default().AckReauth(authID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no team-share state for that credential"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type teamShareMemberRequest struct {
	APIKey  string  `json:"api_key"`
	KeyID   string  `json:"key_id"`
	Percent float64 `json:"percent"`
}

type teamShareAccountRequest struct {
	Auth string `json:"auth"`
	Tier string `json:"tier"`
}

func clonedTeamShare(src config.TeamShareConfig) config.TeamShareConfig {
	out := src
	out.Pools = make([]config.TeamSharePool, len(src.Pools))
	for i, pool := range src.Pools {
		out.Pools[i] = pool
		out.Pools[i].Accounts = append([]config.TeamShareAccount(nil), pool.Accounts...)
		out.Pools[i].Members = append([]config.TeamShareMember(nil), pool.Members...)
	}
	return out
}

func findPool(cfg *config.TeamShareConfig, name string) int {
	for i := range cfg.Pools {
		if strings.EqualFold(cfg.Pools[i].Name, name) {
			return i
		}
	}
	return -1
}

// mutateTeamShare edits a validated copy of the team-share config and persists it only when valid.
func (h *Handler) mutateTeamShare(c *gin.Context, edit func(*config.TeamShareConfig) (int, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	candidate := clonedTeamShare(h.cfg.TeamShare)
	if status, err := edit(&candidate); err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if err := candidate.Validate(); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	h.cfg.TeamShare = candidate
	h.persistLocked(c)
}

// PutTeamShareMember adds or updates a member's percentage. A raw api_key is stored as its key-id.
func (h *Handler) PutTeamShareMember(c *gin.Context) {
	var req teamShareMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	poolName, name := c.Param("pool"), strings.TrimSpace(c.Param("name"))
	h.mutateTeamShare(c, func(ts *config.TeamShareConfig) (int, error) {
		idx := findPool(ts, poolName)
		if idx < 0 {
			ts.Pools = append(ts.Pools, config.TeamSharePool{Name: poolName})
			idx = len(ts.Pools) - 1
		}
		member := config.TeamShareMember{Name: name, KeyID: strings.TrimSpace(req.KeyID), Percent: req.Percent}
		if key := strings.TrimSpace(req.APIKey); key != "" {
			member.KeyID = teamshare.KeyID(key)
		}
		members := ts.Pools[idx].Members
		for i := range members {
			if strings.EqualFold(members[i].Name, name) {
				if member.KeyID == "" {
					member.KeyID = members[i].KeyID
					member.APIKey = members[i].APIKey
				}
				members[i] = member
				return 0, nil
			}
		}
		ts.Pools[idx].Members = append(members, member)
		return 0, nil
	})
}

// DeleteTeamShareMember removes a member; the freed percentage becomes unassigned.
func (h *Handler) DeleteTeamShareMember(c *gin.Context) {
	poolName, name := c.Param("pool"), strings.TrimSpace(c.Param("name"))
	h.mutateTeamShare(c, func(ts *config.TeamShareConfig) (int, error) {
		idx := findPool(ts, poolName)
		if idx < 0 {
			return http.StatusNotFound, errTeamSharePoolNotFound
		}
		kept := ts.Pools[idx].Members[:0:0]
		found := false
		for _, m := range ts.Pools[idx].Members {
			if strings.EqualFold(m.Name, name) {
				found = true
				continue
			}
			kept = append(kept, m)
		}
		if !found {
			return http.StatusNotFound, errTeamShareMemberNotFound
		}
		ts.Pools[idx].Members = kept
		return 0, nil
	})
}

// PutTeamShareAccount adds a credential to a pool or changes its seat tier.
func (h *Handler) PutTeamShareAccount(c *gin.Context) {
	var req teamShareAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Auth) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is required"})
		return
	}
	poolName := c.Param("pool")
	h.mutateTeamShare(c, func(ts *config.TeamShareConfig) (int, error) {
		idx := findPool(ts, poolName)
		if idx < 0 {
			ts.Pools = append(ts.Pools, config.TeamSharePool{Name: poolName})
			idx = len(ts.Pools) - 1
		}
		account := config.TeamShareAccount{Auth: strings.TrimSpace(req.Auth), Tier: config.NormalizeTeamShareTier(req.Tier)}
		accounts := ts.Pools[idx].Accounts
		for i := range accounts {
			if strings.EqualFold(accounts[i].Auth, account.Auth) {
				accounts[i] = account
				return 0, nil
			}
		}
		ts.Pools[idx].Accounts = append(accounts, account)
		return 0, nil
	})
}

// DeleteTeamShareAccount removes a credential from a pool, which shrinks the pool total.
func (h *Handler) DeleteTeamShareAccount(c *gin.Context) {
	poolName, auth := c.Param("pool"), strings.TrimSpace(c.Query("auth"))
	h.mutateTeamShare(c, func(ts *config.TeamShareConfig) (int, error) {
		idx := findPool(ts, poolName)
		if idx < 0 {
			return http.StatusNotFound, errTeamSharePoolNotFound
		}
		kept := ts.Pools[idx].Accounts[:0:0]
		found := false
		for _, a := range ts.Pools[idx].Accounts {
			if strings.EqualFold(a.Auth, auth) {
				found = true
				continue
			}
			kept = append(kept, a)
		}
		if !found {
			return http.StatusNotFound, errTeamShareAccountNotFound
		}
		ts.Pools[idx].Accounts = kept
		return 0, nil
	})
}
