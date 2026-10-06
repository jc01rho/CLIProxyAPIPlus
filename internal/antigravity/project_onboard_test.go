package antigravity

import "testing"

func TestAntigravityOnboardRequestBodyUsesSnakeCaseTierAndMetadata(t *testing.T) {
	body := antigravityOnboardRequestBody("free-tier", "antigravity/hub/2.9.1 darwin/arm64 google-api-nodejs-client/10.3.0")
	if got, ok := body["tier_id"].(string); !ok || got != "free-tier" {
		t.Fatalf("tier_id = %#v", body["tier_id"])
	}
	if _, ok := body["tierId"]; ok {
		t.Fatalf("camelCase tierId must not be sent: %#v", body["tierId"])
	}
	meta, ok := body["metadata"].(map[string]string)
	if !ok {
		t.Fatalf("metadata = %#v", body["metadata"])
	}
	if meta["ide_type"] != "ANTIGRAVITY" || meta["ide_name"] != "antigravity" || meta["ide_version"] != "2.9.1" {
		t.Fatalf("metadata = %#v", meta)
	}
}
