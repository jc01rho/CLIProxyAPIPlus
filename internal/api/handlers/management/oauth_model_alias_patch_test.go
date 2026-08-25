package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestPatchOAuthModelAlias_PreservesDuplicateAliasesWithExistingSection
// mirrors the production scenario: config.yaml ALREADY contains an
// oauth-model-alias.kilo section (user-reported live config) and the
// management editor PATCHes the channel with rows that include a duplicate
// alias. The on-disk YAML must keep every row.
func TestPatchOAuthModelAlias_PreservesDuplicateAliasesWithExistingSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	existing := `port: 8317
oauth-model-alias:
  kilo:
    - name: tencent/hy3:free
      alias: lower-coding
      fork: true
    - name: meituan/longcat-2.0-free
      alias: higher-coding
      fork: true
`
	if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("write seed config: %v", err)
	}

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: cfgPath,
		authManager:    coreauth.NewManager(nil, nil, nil),
	}

	payload := `{"channel":"kilo","aliases":[` +
		`{"name":"tencent/hy3:free","alias":"lower-coding","fork":true},` +
		`{"name":"meituan/longcat-2.0-free","alias":"higher-coding","fork":true},` +
		`{"name":"openrouter/deepseek-r1:free","alias":"lower-coding","fork":true}]}`

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/oauth-model-alias", strings.NewReader(payload))

	h.PatchOAuthModelAlias(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.OAuthModelAlias["kilo"]); got != 3 {
		t.Fatalf("in-memory kilo aliases = %#v, want 3 rows", h.cfg.OAuthModelAlias["kilo"])
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if got := strings.Count(string(data), "alias: lower-coding"); got != 2 {
		t.Fatalf("on-disk config has %d occurrences of \"alias: lower-coding\", want 2;\n%s", got, string(data))
	}
	if got := strings.Count(string(data), "alias: higher-coding"); got != 1 {
		t.Fatalf("on-disk config has %d occurrences of \"alias: higher-coding\", want 1;\n%s", got, string(data))
	}
}
