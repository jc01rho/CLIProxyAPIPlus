package registry

import "testing"

func TestClineHasNoStaticCatalog(t *testing.T) {
	if models := GetStaticModelDefinitionsByChannel("cline"); models == nil || len(models) != 0 {
		t.Fatalf("static Cline models = %v, want a known channel with an empty catalog", models)
	}
}
