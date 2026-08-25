package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestPutOAuthModelAlias_PersistsDuplicateAliasesToFile guards the full
// management save chain for duplicate aliases within a channel: PUT payload
// -> sanitizedOAuthModelAlias -> SaveConfigPreserveComments -> the on-disk
// YAML keeps every entry. Regression guard for the "backend silently drops
// duplicates" class of bugs.
func TestPutOAuthModelAlias_PersistsDuplicateAliasesToFile(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
		authManager:    coreauth.NewManager(nil, nil, nil),
	}

	payload := `{"claude":[` +
		`{"name":"claude-opus-4-8","alias":"prio","fork":false},` +
		`{"name":"claude-sonnet-4-5","alias":"prio","fork":false}]}`

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/oauth-model-alias", strings.NewReader(payload))

	h.PutOAuthModelAlias(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	aliases := h.cfg.OAuthModelAlias["claude"]
	if len(aliases) != 2 {
		t.Fatalf("in-memory aliases = %#v, want 2 entries sharing one alias", aliases)
	}
	if aliases[0].Alias != "prio" || aliases[1].Alias != "prio" {
		t.Fatalf("in-memory aliases = %#v, want both alias fields preserved", aliases)
	}

	data, err := os.ReadFile(h.configFilePath)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if got := strings.Count(string(data), "alias: prio"); got != 2 {
		t.Fatalf("on-disk config has %d occurrences of \"alias: prio\", want 2;\n%s", got, string(data))
	}
}
