package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	managementHandlerPattern      = regexp.MustCompile(`(?m)^func \(h \*Handler\) ([A-Z][A-Za-z0-9]*)\([^)]*\*gin\.Context`)
	managementRegistrationPattern = regexp.MustCompile(`s\.mgmt\.([A-Z][A-Za-z0-9]*)`)
	routeLiteralPattern           = regexp.MustCompile(`\b([a-zA-Z_][a-zA-Z0-9_]*)\.(GET|POST|PUT|PATCH|DELETE)\("([^"]+)"`)
)

// readNonTestGoSources returns the concatenated production Go source under dir.
func readNonTestGoSources(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			return errRead
		}
		sb.Write(data)
		sb.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	return sb.String()
}

func submatchSet(pattern *regexp.Regexp, src string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range pattern.FindAllStringSubmatch(src, -1) {
		out[m[1]] = struct{}{}
	}
	return out
}

// An upstream merge can move route registration between files and silently
// drop a fork handler; every management handler must stay registered.
func TestEveryManagementHandlerIsRegistered(t *testing.T) {
	handlers := submatchSet(managementHandlerPattern, readNonTestGoSources(t, filepath.Join("handlers", "management")))
	registered := submatchSet(managementRegistrationPattern, readNonTestGoSources(t, "."))
	if len(handlers) == 0 || len(registered) == 0 {
		t.Fatalf("scan is broken, not the routes: handlers=%d registered=%d", len(handlers), len(registered))
	}
	var missing []string
	for name := range handlers {
		if _, ok := registered[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("management handlers without a s.mgmt.<Handler> registration: %v", missing)
	}
}

// Two registrations of the same method and path on one router group make gin
// panic at startup or shadow one another after a merge.
func TestNoDuplicateRouteRegistrations(t *testing.T) {
	counts := map[string]int{}
	for _, m := range routeLiteralPattern.FindAllStringSubmatch(readNonTestGoSources(t, "."), -1) {
		counts[m[1]+"."+m[2]+" "+m[3]]++
	}
	if len(counts) == 0 {
		t.Fatal("scan is broken, not the routes: no route literals found")
	}
	var dups []string
	for key, n := range counts {
		if n > 1 {
			dups = append(dups, key)
		}
	}
	sort.Strings(dups)
	if len(dups) > 0 {
		t.Fatalf("duplicate group+method+path registrations: %v", dups)
	}
}
