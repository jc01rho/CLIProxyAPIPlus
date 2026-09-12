package executor

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Live check: set DEVIN_CRED to the credential file path.
func TestDevinLiveCatalogDiscovery(t *testing.T) {
	path := os.Getenv("DEVIN_CRED")
	if path == "" {
		t.Skip("set DEVIN_CRED to run live catalog discovery")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cred: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse cred: %v", err)
	}
	auth := &cliproxyauth.Auth{ID: "devin-live", Provider: "devin", Metadata: doc}
	models := FetchDevinModels(context.Background(), auth, nil)
	if len(models) == 0 {
		t.Fatal("discovery returned no models")
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	swe := 0
	for _, id := range ids {
		if len(id) >= 3 && id[:3] == "swe" {
			swe++
		}
	}
	t.Logf("discovered %d models, %d swe-*", len(models), swe)
	for _, id := range ids {
		if len(id) >= 5 && id[:5] == "swe-2" {
			t.Logf("  swe-2 family: %s", id)
		}
	}
	if swe == 0 {
		t.Error("no swe-* models in the discovered catalog")
	}
}
