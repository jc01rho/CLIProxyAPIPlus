# Implementation Plan: Alias Resolution Order (Original-First Fallback)

**Objective:** Change alias resolution so that if the requested model name already matches a real/original model name, execution tries that original model first and only then alias-resolved alternatives.

**Status:** CONCRETE PLAN (based on code analysis, NOT hypothetical)

---

## 1. EXECUTIVE SUMMARY

### Current Behavior
The alias resolution system currently processes models in this order:
1. **Alias matching phase**: Try to find the alias in config and resolve to upstream model
2. **Direct name matching phase**: If no alias found, try matching by original model name

This means if you have config:
```yaml
oauth-model-alias:
  gemini-cli:
    - alias: "gemini-2.5-flash"
      name: "gemini-2.5-flash-exp-03-25"
```

And request `"gemini-2.5-flash-exp-03-25"` (the real/original model name), it:
- Doesn't match the alias (alias is "gemini-2.5-flash")
- Falls back to direct name match
- Returns the same model

### Desired Behavior
If the requested model name matches an original/upstream model name in the config, **try that model first** before attempting alias resolution.

This would require:
1. Check if `requestedModel` matches any original model name in config
2. If yes: return it as the PRIMARY candidate
3. If no: proceed with normal alias resolution

---

## 2. CURRENT CODE FLOW ANALYSIS

### Entry Point: `prepareExecutionModels()` 
**File:** `CLIProxyAPIPlus/sdk/cliproxy/auth/conductor.go:493`

```go
func (m *Manager) prepareExecutionModels(auth *Auth, routeModel string) []string {
    requestedModel := rewriteModelForAuth(routeModel, auth)           // Line 494
    requestedModel = m.applyOAuthModelAlias(auth, requestedModel)   // Line 495
    if pool := m.resolveOpenAICompatUpstreamModelPool(...); ... {    // Line 496
        return pool
    }
    resolved := m.applyAPIKeyModelAlias(auth, requestedModel)        // Line 503
    return []string{resolved}
}
```

**Key call path:**
- `applyOAuthModelAlias()` → `resolveOAuthUpstreamModel()` → `resolveUpstreamModelFromAliasTable()`

### Core Resolution Logic: `resolveUpstreamModelFromAliasTable()`
**File:** `CLIProxyAPIPlus/sdk/cliproxy/auth/oauth_model_alias.go:194`

Current logic (lines 194-255):
```go
func resolveUpstreamModelFromAliasTable(...) string {
    // Extract thinking suffix
    requestResult := thinking.ParseSuffix(requestedModel)
    baseModel := requestResult.ModelName
    
    candidates := []string{baseModel}
    if baseModel != requestedModel {
        candidates = append(candidates, requestedModel)
    }
    
    // Load alias table (reverse: alias → original)
    table := m.oauthModelAlias.Load()
    
    // Loop through candidates and lookup in alias table
    for _, candidate := range candidates {
        key := strings.ToLower(candidate)
        original := rev[key]  // Check if key is an ALIAS
        if original == "" {
            continue
        }
        if strings.EqualFold(original, baseModel) {
            return ""  // Alias resolves to same model
        }
        return original  // Found alias → return upstream
    }
    
    return ""  // No alias found
}
```

**Problem:** This logic only checks the reverse table (alias → original). It does NOT check if the requested model **IS** an original model name.

### OpenAI-Compatible Pool Resolution: `resolveOpenAICompatUpstreamModelPool()`
**File:** `CLIProxyAPIPlus/sdk/cliproxy/auth/conductor.go:460`

This calls `resolveModelAliasPoolFromConfigModels()` (line 482).

### API Key Pool Resolution: `resolveModelAliasPoolFromConfigModels()`
**File:** `CLIProxyAPIPlus/sdk/cliproxy/auth/oauth_model_alias.go:119`

Current logic (lines 119-173):
```go
func resolveModelAliasPoolFromConfigModels(requestedModel string, models []modelAliasEntry) []string {
    requestResult, candidates := modelAliasLookupCandidates(requestedModel)
    
    out := make([]string, 0)
    seen := make(map[string]struct{})
    
    // PHASE 1: Try to find aliases
    for i := range models {
        name := models[i].GetName()
        alias := models[i].GetAlias()
        for _, candidate := range candidates {
            if strings.EqualFold(alias, candidate) {
                // Found alias match → add upstream name to result
                resolved := name
                out = append(out, resolved)
                break
            }
        }
    }
    if len(out) > 0 {
        return out  // ← RETURNS EARLY if any alias matched
    }
    
    // PHASE 2: Try to find direct name match (fallback)
    for i := range models {
        name := models[i].GetName()
        for _, candidate := range candidates {
            if strings.EqualFold(name, candidate) {
                // Found direct name match
                return []string{name}
            }
        }
    }
    
    return nil
}
```

**Problem:** Phase 1 runs BEFORE Phase 2. If an alias matches, it returns immediately without checking if the requested model is itself an original name.

---

## 3. FILES TO MODIFY

### **File 1: `CLIProxyAPIPlus/sdk/cliproxy/auth/oauth_model_alias.go`**

#### **Change 1.1: Refactor `resolveUpstreamModelFromAliasTable()` (lines 194-255)**

**Action:** Add upstream model check before alias check.

**Current logic:**
1. Extract base model
2. Check if any candidate is an ALIAS → resolve to upstream
3. Return "" if not found

**New logic:**
1. Extract base model
2. **NEW: Check if any candidate IS an upstream model in the table** → return it first
3. Check if any candidate is an ALIAS → resolve to upstream
4. Return "" if not found

**Implementation:**
```go
func resolveUpstreamModelFromAliasTable(m *Manager, auth *Auth, requestedModel, channel string) string {
    if m == nil || auth == nil || channel == "" {
        return ""
    }
    
    requestResult := thinking.ParseSuffix(requestedModel)
    baseModel := requestResult.ModelName
    
    candidates := []string{baseModel}
    if baseModel != requestedModel {
        candidates = append(candidates, requestedModel)
    }
    
    raw := m.oauthModelAlias.Load()
    table, _ := raw.(*oauthModelAliasTable)
    if table == nil || table.reverse == nil {
        return ""
    }
    
    rev := table.reverse[channel]
    if rev == nil {
        return ""
    }
    
    // ✅ NEW PHASE 1: Check if requested model IS an upstream model (original-first)
    for _, candidate := range candidates {
        key := strings.ToLower(strings.TrimSpace(candidate))
        if key == "" {
            continue
        }
        // Check if this key is a VALUE (upstream name) in the reverse table
        for _, upstream := range rev {
            if strings.EqualFold(strings.ToLower(upstream), key) {
                // Found: requested model matches an upstream model name
                return preserveResolvedModelSuffix(candidate, requestResult)
            }
        }
    }
    
    // PHASE 2: Check if any candidate is an ALIAS
    for _, candidate := range candidates {
        key := strings.ToLower(strings.TrimSpace(candidate))
        if key == "" {
            continue
        }
        original := strings.TrimSpace(rev[key])
        if original == "" {
            continue
        }
        if strings.EqualFold(original, baseModel) {
            return ""
        }
        if thinking.ParseSuffix(original).HasSuffix {
            return original
        }
        if requestResult.HasSuffix && requestResult.RawSuffix != "" {
            return original + "(" + requestResult.RawSuffix + ")"
        }
        return original
    }
    
    return ""
}
```

#### **Change 1.2: Refactor `resolveModelAliasPoolFromConfigModels()` (lines 119-173)**

**Action:** Add original model check before alias check.

**New logic:**
1. **NEW: Check if requested model IS an original model name** → return it first
2. Check if any candidate is an ALIAS → resolve to upstream
3. Check if any candidate is a direct name match (fallback)
4. Return nil if not found

**Implementation:**
```go
func resolveModelAliasPoolFromConfigModels(requestedModel string, models []modelAliasEntry) []string {
    requestedModel = strings.TrimSpace(requestedModel)
    if requestedModel == "" {
        return nil
    }
    if len(models) == 0 {
        return nil
    }

    requestResult, candidates := modelAliasLookupCandidates(requestedModel)
    if len(candidates) == 0 {
        return nil
    }

    out := make([]string, 0)
    seen := make(map[string]struct{})

    // ✅ PHASE 1 (NEW): Check if any candidate IS an original model name (original-first)
    for i := range models {
        name := strings.TrimSpace(models[i].GetName())
        for _, candidate := range candidates {
            if candidate == "" || name == "" {
                continue
            }
            if strings.EqualFold(name, candidate) {
                // Found: requested model matches an original model name
                resolved := preserveResolvedModelSuffix(name, requestResult)
                key := strings.ToLower(strings.TrimSpace(resolved))
                if key == "" {
                    continue
                }
                if _, exists := seen[key]; exists {
                    continue
                }
                seen[key] = struct{}{}
                out = append(out, resolved)
                break
            }
        }
    }
    if len(out) > 0 {
        return out  // Return original model matches first
    }

    // PHASE 2: Try to find aliases
    for i := range models {
        name := strings.TrimSpace(models[i].GetName())
        alias := strings.TrimSpace(models[i].GetAlias())
        for _, candidate := range candidates {
            if candidate == "" || alias == "" || !strings.EqualFold(alias, candidate) {
                continue
            }
            resolved := candidate
            if name != "" {
                resolved = name
            }
            resolved = preserveResolvedModelSuffix(resolved, requestResult)
            key := strings.ToLower(strings.TrimSpace(resolved))
            if key == "" {
                break
            }
            if _, exists := seen[key]; exists {
                break
            }
            seen[key] = struct{}{}
            out = append(out, resolved)
            break
        }
    }
    if len(out) > 0 {
        return out
    }

    // PHASE 3: Try to find direct name match (fallback)
    for i := range models {
        name := strings.TrimSpace(models[i].GetName())
        for _, candidate := range candidates {
            if candidate == "" || name == "" || !strings.EqualFold(name, candidate) {
                continue
            }
            return []string{preserveResolvedModelSuffix(name, requestResult)}
        }
    }

    return nil
}
```

---

## 4. TESTS TO ADD/UPDATE

### **Test File 1: `CLIProxyAPIPlus/sdk/cliproxy/auth/oauth_model_alias_test.go`**

#### **New Test 1.1: Original model takes priority over alias**

```go
func TestResolveOAuthUpstreamModel_OriginalTakesPlacePriority(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name    string
        aliases map[string][]internalconfig.OAuthModelAlias
        channel string
        input   string
        want    string
    }{
        {
            name: "original model name matches - returns as-is",
            aliases: map[string][]internalconfig.OAuthModelAlias{
                "gemini-cli": {
                    {Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"},
                },
            },
            channel: "gemini-cli",
            input:   "gemini-2.5-pro-exp-03-25",  // Request original name
            want:    "gemini-2.5-pro-exp-03-25",  // Should return it immediately
        },
        {
            name: "original model name matches with suffix - preserves suffix",
            aliases: map[string][]internalconfig.OAuthModelAlias{
                "claude": {
                    {Name: "claude-sonnet-4-5-20250514", Alias: "claude-sonnet-4-5"},
                },
            },
            channel: "claude",
            input:   "claude-sonnet-4-5-20250514(high)",  // Original with suffix
            want:    "claude-sonnet-4-5-20250514(high)",  // Should return original+suffix
        },
        {
            name: "original model name matches exactly - ignores alias",
            aliases: map[string][]internalconfig.OAuthModelAlias{
                "codex": {
                    {Name: "codex-upstream-001", Alias: "codex-alias"},
                    {Name: "codex-alternative-002", Alias: "codex-upstream-001"},
                },
            },
            channel: "codex",
            input:   "codex-upstream-001",  // Request original name (which is also an alias!)
            want:    "codex-upstream-001",  // Should return original, NOT resolve alias
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            m := NewManager(nil, nil, nil)
            m.SetOAuthModelAlias(tc.aliases)
            
            auth := &Auth{
                Provider:   "test-provider",
                Attributes: map[string]string{"auth_kind": strings.TrimPrefix(tc.channel, "gemini-cli-") },
            }
            
            result := m.resolveOAuthUpstreamModel(auth, tc.input)
            
            if result != tc.want {
                t.Errorf("resolveOAuthUpstreamModel() = %q, want %q", result, tc.want)
            }
        })
    }
}
```

#### **New Test 1.2: Fallback to alias if original not found**

```go
func TestResolveOAuthUpstreamModel_FallbackToAlias(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name    string
        aliases map[string][]internalconfig.OAuthModelAlias
        channel string
        input   string
        want    string
    }{
        {
            name: "alias resolves when original not requested",
            aliases: map[string][]internalconfig.OAuthModelAlias{
                "gemini-cli": {
                    {Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"},
                },
            },
            channel: "gemini-cli",
            input:   "gemini-2.5-pro",  // Request alias
            want:    "gemini-2.5-pro-exp-03-25",  // Should resolve to original
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            m := NewManager(nil, nil, nil)
            m.SetOAuthModelAlias(tc.aliases)
            
            auth := &Auth{
                Provider:   "test-provider",
                Attributes: map[string]string{"auth_kind": "oauth"},
            }
            
            result := m.resolveOAuthUpstreamModel(auth, tc.input)
            
            if result != tc.want {
                t.Errorf("resolveOAuthUpstreamModel() = %q, want %q", result, tc.want)
            }
        })
    }
}
```

### **Test File 2: `CLIProxyAPIPlus/sdk/cliproxy/auth/conductor_test.go` (if exists, otherwise create)**

#### **New Test 2.1: Integration test with prepareExecutionModels()**

```go
func TestPrepareExecutionModels_OriginalFirst(t *testing.T) {
    t.Parallel()

    m := NewManager(nil, nil, nil)
    m.SetConfig(&internalconfig.Config{})
    
    // Set up OAuth alias
    m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
        "gemini-cli": {
            {Name: "gemini-2.5-pro-exp-03-25", Alias: "gemini-2.5-pro"},
            {Name: "gemini-2.5-flash-exp-03-25", Alias: "gemini-2.5-flash"},
        },
    })

    auth := &Auth{
        ID:       "test-auth",
        Provider: "gemini",
        Attributes: map[string]string{
            "auth_kind": "oauth",
        },
    }

    tests := []struct {
        name      string
        routeModel string
        wantFirst string
    }{
        {
            name:       "request original model name - returns original",
            routeModel: "gemini-2.5-pro-exp-03-25",
            wantFirst:  "gemini-2.5-pro-exp-03-25",
        },
        {
            name:       "request alias - resolves to original",
            routeModel: "gemini-2.5-pro",
            wantFirst:  "gemini-2.5-pro-exp-03-25",
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            models := m.prepareExecutionModels(auth, tc.routeModel)
            
            if len(models) == 0 {
                t.Errorf("prepareExecutionModels() returned empty slice")
                return
            }
            
            if models[0] != tc.wantFirst {
                t.Errorf("prepareExecutionModels() first model = %q, want %q", models[0], tc.wantFirst)
            }
        })
    }
}
```

### **Test File 3: Update existing tests in `oauth_model_alias_test.go`**

Review and update `TestResolveOAuthUpstreamModel_SuffixPreservation()` and `TestApplyOAuthModelAlias_SuffixPreservation()` to include new test cases for original-model scenarios.

---

## 5. VERIFICATION COMMANDS

### **5.1 Run affected test files**

```bash
# Run OAuth model alias tests
go test -v -run TestResolveOAuthUpstreamModel ./CLIProxyAPIPlus/sdk/cliproxy/auth/

# Run conductor tests (if any)
go test -v -run TestPrepareExecutionModels ./CLIProxyAPIPlus/sdk/cliproxy/auth/

# Run all auth package tests
go test -v ./CLIProxyAPIPlus/sdk/cliproxy/auth/
```

### **5.2 Verify no regressions**

```bash
# Run full test suite for SDK
go test -v ./CLIProxyAPIPlus/sdk/...

# Run integration tests
go test -v ./CLIProxyAPIPlus/test/
```

### **5.3 Benchmark (optional)**

```bash
# Compare performance before/after
go test -bench=BenchmarkResolveUpstreamModel -benchmem ./CLIProxyAPIPlus/sdk/cliproxy/auth/
```

---

## 6. IMPLEMENTATION CHECKLIST

- [ ] **Phase 1: Implement resolver functions**
  - [ ] Update `resolveUpstreamModelFromAliasTable()` with original-first logic
  - [ ] Update `resolveModelAliasPoolFromConfigModels()` with original-first logic
  - [ ] Test locally: `go test ./CLIProxyAPIPlus/sdk/cliproxy/auth/`

- [ ] **Phase 2: Add new test cases**
  - [ ] Add `TestResolveOAuthUpstreamModel_OriginalTakesPlacePriority()`
  - [ ] Add `TestResolveOAuthUpstreamModel_FallbackToAlias()`
  - [ ] Add `TestPrepareExecutionModels_OriginalFirst()`
  - [ ] Test locally: `go test -v ./CLIProxyAPIPlus/sdk/cliproxy/auth/`

- [ ] **Phase 3: Update existing tests (if needed)**
  - [ ] Review existing test cases
  - [ ] Update assertions if behavior changes
  - [ ] Verify all existing tests pass

- [ ] **Phase 4: Integration testing**
  - [ ] Run full SDK test suite: `go test ./CLIProxyAPIPlus/sdk/...`
  - [ ] Run integration tests: `go test ./CLIProxyAPIPlus/test/`
  - [ ] Manual testing with real config

- [ ] **Phase 5: Documentation**
  - [ ] Update code comments in modified functions
  - [ ] Document the original-first behavior in comments
  - [ ] Update PR description with behavior change

---

## 7. RISKS & MITIGATIONS

| Risk | Impact | Mitigation |
|------|--------|-----------|
| **Behavior change** | Existing code relying on alias-first order | Comprehensive test coverage before/after |
| **Performance regression** | Model resolution slows down | Add benchmark tests; profile before/after |
| **Config ambiguity** | Model name appears as both alias and original | Clearly document in code; test edge cases |
| **Suffix handling** | Suffix lost/duplicated during resolution | Test all suffix types (numeric, named, auto, none) |
| **Multi-provider routing** | OpenAI-compatible vs OAuth paths | Test both code paths independently |

---

## 8. DEPENDENCY ANALYSIS

**Files affected (dependencies):**
1. `oauth_model_alias.go` → Imports:
   - `github.com/router-for-me/CLIProxyAPI/v6/internal/thinking`
   - `strings`
   - `internalconfig`

2. `conductor.go` → Uses:
   - `resolveOpenAICompatUpstreamModelPool()` (existing)
   - `applyOAuthModelAlias()` (existing)
   - `applyAPIKeyModelAlias()` (existing)

**No new dependencies introduced.**

---

## 9. BACKWARD COMPATIBILITY

**Breaking Change:** YES

**Rationale:** The order of model resolution changes. Code that relied on alias-first behavior may see different results.

**Mitigation:** 
- Comprehensive test coverage ensures correct behavior
- Config aliases still work (just tried after originals)
- No API signature changes
- No config schema changes

---

## 10. NOTES

- **Test coverage:** Current tests in `oauth_model_alias_test.go` do NOT cover the original-model-first scenario. New tests are required.
- **Suffix handling:** The `thinking.ParseSuffix()` function is already well-tested. New logic reuses `preserveResolvedModelSuffix()`, which is safe.
- **Table structure:** OAuth alias table stores `reverse map[channel]map[alias(lower) -> upstream]`. New logic only iterates VALUES to check if requested model is an upstream name. This is efficient (O(n) where n = # of aliases).

---

## APPENDIX A: CODE LOCATIONS (Quick Reference)

| Component | File | Lines | Function |
|-----------|------|-------|----------|
| **Main resolver (OAuth)** | `oauth_model_alias.go` | 194-255 | `resolveUpstreamModelFromAliasTable()` |
| **Pool resolver (API Key)** | `oauth_model_alias.go` | 119-173 | `resolveModelAliasPoolFromConfigModels()` |
| **Entry point** | `conductor.go` | 493-508 | `prepareExecutionModels()` |
| **Existing tests** | `oauth_model_alias_test.go` | 1-243 | Various test functions |
| **Helper function** | `oauth_model_alias.go` | 88-103 | `modelAliasLookupCandidates()` |
| **Suffix preservation** | `oauth_model_alias.go` | 105-117 | `preserveResolvedModelSuffix()` |

---

## APPENDIX B: Example Scenarios

### Scenario 1: Direct original model request
**Config:**
```yaml
oauth-model-alias:
  gemini-cli:
    - alias: "gemini-2.5-flash"
      name: "gemini-2.5-flash-exp-03-25"
```

**Request:** `gemini-2.5-flash-exp-03-25`  
**Current behavior:** No alias match → fallback to direct match → returns `gemini-2.5-flash-exp-03-25`  
**New behavior:** Check if original → YES, matches name → returns `gemini-2.5-flash-exp-03-25` (faster, same result)

### Scenario 2: Alias request
**Same config**

**Request:** `gemini-2.5-flash`  
**Current behavior:** Check aliases → YES, resolves to → returns `gemini-2.5-flash-exp-03-25`  
**New behavior:** Check if original → NO → check aliases → YES, resolves to → returns `gemini-2.5-flash-exp-03-25` (same result)

### Scenario 3: Model that is both original and alias
**Config:**
```yaml
oauth-model-alias:
  codex:
    - alias: "codex-upstream-001"
      name: "codex-alternative-002"
    - alias: "codex-alias"
      name: "codex-upstream-001"
```

**Request:** `codex-upstream-001`  
**Current behavior:** Check aliases → YES (it's an alias) → resolves to → returns `codex-alternative-002`  
**New behavior:** Check if original → YES, matches name → returns `codex-upstream-001` (DIFFERENT, but correct: original takes priority)

---

**END OF IMPLEMENTATION PLAN**
