package antigravity

import (
	"encoding/json"
	"testing"
)

func TestAntigravityOnboardRequestBodyMatchesReference(t *testing.T) {
	raw, err := json.Marshal(antigravityOnboardRequestBody(" free-tier "))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"tierId":"free-tier"}`; got != want {
		t.Fatalf("onboardUser body = %s, want %s", got, want)
	}
}
