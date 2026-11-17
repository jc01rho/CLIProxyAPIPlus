package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fork-only conductor behaviour that an upstream merge can drop without a
// compile error. The names are the stable entry points of each feature.
var forkOnlyConductorFuncs = []string{
	// 429/503/504 provider failover budget
	"shouldPreserveAttemptBudgetForStatus", "shouldCountAttemptBudget", "countRemainingProviderOptions", "logProviderFallbackRetry",
	// route fallback
	"executeWithRouteFallback", "executeStreamWithRouteFallback",
	// threshold routing and billing class
	"SetBillingDecisionInContext",
	// failure logging
	"resultFailureLogFields",
	// OAuth alias
	"resolveBlockedForkAliasTarget", "authSupportsExplicitOAuthAliasWithoutRegistry",
	// cooldown persistence
	"SetCooldownStateStore",
	// antigravity sole-primary reconcile
	"reconcileSoleAntigravityPrimaryLocked",
}

func declaredFuncNames(t *testing.T) map[string]struct{} {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]struct{}{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		parsed, errParse := parser.ParseFile(fset, file, src, 0)
		if errParse != nil {
			t.Fatalf("parse %s: %v", file, errParse)
		}
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				names[fn.Name.Name] = struct{}{}
			}
		}
	}
	return names
}

func TestForkOnlyConductorFeaturesSurvive(t *testing.T) {
	names := declaredFuncNames(t)
	if len(names) == 0 {
		t.Fatal("scan is broken: no functions found")
	}
	var missing []string
	for _, want := range forkOnlyConductorFuncs {
		if _, ok := names[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("fork-only conductor functions are gone (restore from the last known-good commit): %v", missing)
	}
	affinity := 0
	for name := range names {
		if strings.HasPrefix(name, "sessionModelAffinity") {
			affinity++
		}
	}
	if affinity == 0 {
		t.Fatal("alias pool session pinning (sessionModelAffinity*) is gone")
	}
	if _, err := os.Stat("conductor_threshold.go"); err != nil {
		t.Fatalf("threshold routing file is gone: %v", err)
	}
}
