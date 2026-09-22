package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPutMimocodeKeysRejectsEmptyAPIKey(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/mimocode-api-key", strings.NewReader(`[{"base-url":"https://region.example/v1","models":[{"name":"mimo-v2.5"}]}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutMimocodeKeys(ctx)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "api-key is required") {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestPutAndGetMimocodeKeys(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	putRecorder := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRecorder)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/mimocode-api-key", strings.NewReader(`[{"api-key":"secret","base-url":"https://region.example/v1","models":[{"name":"mimo-v2.5-pro","alias":"pro"}]}]`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutMimocodeKeys(putCtx)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", putRecorder.Code, putRecorder.Body.String())
	}

	getRecorder := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRecorder)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/mimocode-api-key", nil)
	h.GetMimocodeKeys(getCtx)
	if getRecorder.Code != http.StatusOK || !strings.Contains(getRecorder.Body.String(), `"mimocode-api-key"`) || !strings.Contains(getRecorder.Body.String(), `"mimo-v2.5-pro"`) {
		t.Fatalf("GET status/body = %d/%s", getRecorder.Code, getRecorder.Body.String())
	}
}

func TestNormalizeOAuthProviderMimocode(t *testing.T) {
	for _, provider := range []string{"mimocode", "mimo-code"} {
		got, errNormalize := NormalizeOAuthProvider(provider)
		if errNormalize != nil || got != "mimocode" {
			t.Fatalf("NormalizeOAuthProvider(%q) = %q, %v", provider, got, errNormalize)
		}
	}
}
