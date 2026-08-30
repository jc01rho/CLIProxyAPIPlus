package auth

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
)

const oauthModelAliasesAttributeKey = "model_aliases"

// oauthAliasCacheMaxEntries bounds snapshot-local positive and negative alias
// resolutions. Requested model names are client-controlled, so an unbounded
// negative cache would allow a long-lived snapshot to retain arbitrary keys.
const oauthAliasCacheMaxEntries = 1024

type modelAliasEntry interface {
	GetName() string
	GetAlias() string
	GetForceMapping() bool
}

// oauthModelAliasEntry stores the upstream model name and mapping flags for an alias.
type oauthModelAliasEntry struct {
	upstreamModel string
	fork          bool
	configAlias   string
	forceMapping  bool
}

// oauthAliasCacheKey scopes memoized table-only resolutions per channel and
// requested model so distinct channels/auths never contaminate each other.
type oauthAliasCacheKey struct {
	channel        string
	requestedModel string
}

// oauthAliasCacheValue stores the auth-independent part of the alias table
// resolution.
//
// forceMappingHit marks the force-mapping phase result, which the caller returns
// before consulting the auth-specific registry path. tailResult carries the
// upstream-model/alias phase result, which the caller returns after the registry
// path produced nothing. A zero value (forceMappingHit=false, empty tailResult)
// represents a safe negative (miss) entry; values are never nil pointers.
type oauthAliasCacheValue struct {
	forceMappingHit bool
	forceResult    OAuthModelAliasResult
	tailResult     OAuthModelAliasResult
	// tailFork reports whether the phase-2 alias hit is a fork alias. Fork
	// aliases carry isolated per-alias state, so a registry real-model
	// registration under the same name legitimately outranks them (local
	// collision semantics); non-fork aliases share state with the upstream
	// model and must keep resolving to the configured target even when the
	// registry also lists the alias route key as a client model (upstream
	// nofork-alias semantics).
	tailFork bool
}

type oauthModelAliasTable struct {
	// reverse maps channel -> alias (lower) -> entry with upstream model and flags.
	reverse map[string]map[string]oauthModelAliasEntry
	// upstreamIndex maps channel -> set of lowercased upstream model keys. It is
	// precomputed at compile time so the "requested model is an upstream model"
	// phase is a set lookup instead of a per-request scan over every alias entry.
	upstreamIndex map[string]map[string]struct{}

	// cache is a snapshot-local memoization of the auth-independent alias table
	// resolution. It is discarded automatically whenever SetOAuthModelAlias
	// publishes a new table pointer on config reload, so neither positive nor
	// negative entries outlive their snapshot.
	cacheMu sync.RWMutex
	cache   map[oauthAliasCacheKey]oauthAliasCacheValue
	// cacheComputes counts table-only resolutions inserted after cache misses.
	// It is diagnostic-only; the bounded cache may clear and later recompute a key.
	cacheComputes atomic.Int64
}

// OAuthModelAliasResult contains the resolved upstream model and mapping metadata.
type OAuthModelAliasResult struct {
	UpstreamModel string // resolved upstream model name (empty if no mapping found)
	ForceMapping  bool   // whether to rewrite model name in responses
	OriginalAlias string // client-visible model for response rewrite; only applied when ForceMapping is true (see rewriteForceMappedResponse / wrapStreamResult)
}

func compileOAuthModelAliasTable(aliases map[string][]internalconfig.OAuthModelAlias) *oauthModelAliasTable {
	if len(aliases) == 0 {
		return &oauthModelAliasTable{}
	}
	out := &oauthModelAliasTable{
		reverse:       make(map[string]map[string]oauthModelAliasEntry, len(aliases)),
		upstreamIndex: make(map[string]map[string]struct{}, len(aliases)),
	}
	for rawChannel, entries := range aliases {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		rev := make(map[string]oauthModelAliasEntry, len(entries))
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, exists := rev[aliasKey]; exists {
				continue
			}
			rev[aliasKey] = oauthModelAliasEntry{
				upstreamModel: name,
				fork:          entry.Fork,
				configAlias:   alias,
				forceMapping:  entry.ForceMapping,
			}
		}
		if len(rev) > 0 {
			out.reverse[channel] = rev
			out.upstreamIndex[channel] = compileUpstreamModelIndex(rev)
		}
	}
	if len(out.reverse) == 0 {
		out.reverse = nil
		out.upstreamIndex = nil
	}
	return out
}

// compileUpstreamModelIndex builds the set of lowercased upstream model keys for a
// channel's alias entries. It is precomputed once per snapshot so the
// "requested model is itself an upstream model" phase is a set lookup rather than
// a per-request scan over every alias entry.
func compileUpstreamModelIndex(rev map[string]oauthModelAliasEntry) map[string]struct{} {
	index := make(map[string]struct{}, len(rev))
	for _, entry := range rev {
		upstreamKey := strings.ToLower(strings.TrimSpace(entry.upstreamModel))
		if upstreamKey == "" {
			continue
		}
		index[upstreamKey] = struct{}{}
	}
	return index
}

// SetOAuthModelAlias updates the OAuth model name alias table used during execution.
// The alias is applied per-auth channel to resolve the upstream model name while keeping the
// client-visible model name unchanged for translation/response formatting.
func (m *Manager) SetOAuthModelAlias(aliases map[string][]internalconfig.OAuthModelAlias) {
	if m == nil {
		return
	}
	table := compileOAuthModelAliasTable(aliases)
	// atomic.Value requires non-nil store values.
	if table == nil {
		table = &oauthModelAliasTable{}
	}
	m.oauthModelAlias.Store(table)
}

// applyOAuthModelAlias resolves the upstream model from OAuth model alias.
// If an alias exists, the returned model is the upstream model.
func (m *Manager) applyOAuthModelAlias(auth *Auth, requestedModel string) string {
	channel := modelAliasChannel(auth)
	provider, authID := oauthAliasLogIdentity(auth)
	upstreamModel := m.resolveOAuthUpstreamModel(auth, requestedModel)
	if upstreamModel == "" {
		log.Debugf("[DEBUG] applyOAuthModelAlias: provider=%s channel=%s auth_id=%s no alias for model=%s", provider, channel, authID, requestedModel)
		return requestedModel
	}
	log.Debugf("[DEBUG] applyOAuthModelAlias: provider=%s channel=%s auth_id=%s resolved %s -> %s", provider, channel, authID, requestedModel, upstreamModel)
	return upstreamModel
}

// applyOAuthModelAliasStateKey resolves the alias with state-key semantics:
// a configured (non-force) alias target always wins over the auth-specific
// registry lookup's "real registered model → as-is" branch. The registry may
// advertise the alias route key itself as a client model, and returning it
// as-is would skip the alias→target state migration that
// ReconcileRegistryModelStates and MarkResult rely on (upstream
// nofork-alias semantics). Execution-model resolution keeps using
// applyOAuthModelAlias, where the registry lookup decides same-ID
// alias-exposed/real collisions (local real-first rule).
func (m *Manager) applyOAuthModelAliasStateKey(auth *Auth, requestedModel string) string {
	result := m.resolveOAuthModelAliasStateKeyWithResult(auth, requestedModel)
	if result.UpstreamModel != "" {
		return result.UpstreamModel
	}
	return requestedModel
}

// oauthAliasLogIdentity returns only the safe, non-secret identifiers of an auth
// (provider and auth ID) for debug logging. The auth attribute/metadata maps can
// carry credentials and must never be rendered into logs.
func oauthAliasLogIdentity(auth *Auth) (provider, authID string) {
	if auth == nil {
		return "", ""
	}
	return strings.TrimSpace(auth.Provider), strings.TrimSpace(auth.ID)
}

func modelAliasLookupCandidates(requestedModel string) (thinking.SuffixResult, []string) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return thinking.SuffixResult{}, nil
	}
	requestResult := thinking.ParseSuffix(requestedModel)
	base := requestResult.ModelName
	if base == "" {
		base = requestedModel
	}
	candidates := []string{requestedModel}
	if base != requestedModel {
		candidates = append(candidates, base)
	}
	return requestResult, candidates
}

func preserveResolvedModelSuffix(resolved string, requestResult thinking.SuffixResult) string {
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return ""
	}
	if thinking.ParseSuffix(resolved).HasSuffix {
		return resolved
	}
	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		return resolved + "(" + requestResult.RawSuffix + ")"
	}
	return resolved
}

func oauthModelAliasForceMappingResponseModel(configAlias string) string {
	return strings.TrimSpace(configAlias)
}

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

	// PRECEDENCE: Check alias matches FIRST (alias takes priority over direct name).
	// Collect EVERY configured model whose alias matches a request candidate so
	// the resulting pool is the fallback set when the primary upstream fails.
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for i := range models {
			name := strings.TrimSpace(models[i].GetName())
			alias := strings.TrimSpace(models[i].GetAlias())
			if alias == "" || !strings.EqualFold(alias, candidate) {
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
		}
		if len(out) > 0 {
			return out
		}
	}

	// FALLBACK: Check direct name matches SECOND. Direct-name hits return a
	// single-element pool so the alias pool semantics stay intact when the
	// alias phase collected nothing.
	for i := range models {
		name := strings.TrimSpace(models[i].GetName())
		for _, candidate := range candidates {
			if candidate == "" || name == "" || !strings.EqualFold(name, candidate) {
				continue
			}
			return []string{preserveResolvedModelSuffix(name, requestResult)}
		}
	}
	if len(out) > 0 {
		return out
	}

	return nil
}

func resolveModelAliasFromConfigModels(requestedModel string, models []modelAliasEntry) string {
	resolved := resolveModelAliasPoolFromConfigModels(requestedModel, models)
	if len(resolved) > 0 {
		return resolved[0]
	}
	return ""
}

func resolveModelAliasResultFromConfigModels(requestedModel string, models []modelAliasEntry) OAuthModelAliasResult {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || len(models) == 0 {
		return OAuthModelAliasResult{}
	}
	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if len(candidates) == 0 {
		return OAuthModelAliasResult{}
	}
	baseModel := requestResult.ModelName
	if baseModel == "" {
		baseModel = requestedModel
	}
	for _, candidate := range candidates {
		key := strings.TrimSpace(candidate)
		if key == "" {
			continue
		}
		for i := range models {
			original := strings.TrimSpace(models[i].GetName())
			alias := strings.TrimSpace(models[i].GetAlias())
			if original == "" || alias == "" || !strings.EqualFold(alias, key) {
				continue
			}
			if strings.EqualFold(original, baseModel) {
				if !models[i].GetForceMapping() {
					return OAuthModelAliasResult{}
				}
				return OAuthModelAliasResult{
					UpstreamModel: preserveResolvedModelSuffix(original, requestResult),
					ForceMapping:  models[i].GetForceMapping(),
					OriginalAlias: oauthModelAliasForceMappingResponseModel(alias),
				}
			}
			originalAlias := requestedModel
			if models[i].GetForceMapping() {
				originalAlias = oauthModelAliasForceMappingResponseModel(alias)
			}
			return OAuthModelAliasResult{
				UpstreamModel: preserveResolvedModelSuffix(original, requestResult),
				ForceMapping:  models[i].GetForceMapping(),
				OriginalAlias: originalAlias,
			}
		}
	}
	return OAuthModelAliasResult{}
}

// resolveOAuthUpstreamModel resolves the upstream model name from OAuth model alias.
// If an alias exists, returns the original (upstream) model name that corresponds
// to the requested alias.
//
// If the requested model contains a thinking suffix (e.g., "gemini-2.5-pro(8192)"),
// the suffix is preserved in the returned model name. However, if the alias's
// original name already contains a suffix, the config suffix takes priority.
func (m *Manager) resolveOAuthUpstreamModel(auth *Auth, requestedModel string) string {
	result := m.resolveOAuthModelAliasWithResult(auth, requestedModel)
	return result.UpstreamModel
}

func (m *Manager) resolveOAuthModelAliasWithResult(auth *Auth, requestedModel string) OAuthModelAliasResult {
	channel := modelAliasChannel(auth)
	if channel == "" {
		return OAuthModelAliasResult{}
	}
	if result := resolveUpstreamModelFromAliases(OAuthModelAliasesFromAttributes(authAttributes(auth)), requestedModel); result.UpstreamModel != "" {
		return result
	}
	return resolveUpstreamModelFromAliasTable(m, auth, requestedModel, channel)
}

// resolveOAuthModelAliasStateKeyWithResult applies state-key alias semantics
// (see applyOAuthModelAliasStateKey).
func (m *Manager) resolveOAuthModelAliasStateKeyWithResult(auth *Auth, requestedModel string) OAuthModelAliasResult {
	channel := modelAliasChannel(auth)
	if channel == "" {
		return OAuthModelAliasResult{}
	}
	if result := resolveUpstreamModelFromAliases(OAuthModelAliasesFromAttributes(authAttributes(auth)), requestedModel); result.UpstreamModel != "" {
		return result
	}
	return resolveUpstreamModelFromAliasTableStateKey(m, auth, requestedModel, channel)
}

func authAttributes(auth *Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}

// SetOAuthModelAliasesAttribute stores sanitized per-auth OAuth model aliases on an auth entry.
func SetOAuthModelAliasesAttribute(auth *Auth, aliases []internalconfig.OAuthModelAlias) {
	if auth == nil {
		return
	}
	aliases = sanitizeOAuthModelAliases(aliases)
	if len(aliases) == 0 {
		return
	}
	data, errMarshal := json.Marshal(aliases)
	if errMarshal != nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[oauthModelAliasesAttributeKey] = string(data)
}

// OAuthModelAliasesFromAttributes returns sanitized per-auth OAuth model aliases from auth attributes.
func OAuthModelAliasesFromAttributes(attributes map[string]string) []internalconfig.OAuthModelAlias {
	if len(attributes) == 0 {
		return nil
	}
	raw := strings.TrimSpace(attributes[oauthModelAliasesAttributeKey])
	if raw == "" {
		return nil
	}
	var aliases []internalconfig.OAuthModelAlias
	if errUnmarshal := json.Unmarshal([]byte(raw), &aliases); errUnmarshal != nil {
		return nil
	}
	return sanitizeOAuthModelAliases(aliases)
}

func sanitizeOAuthModelAliases(aliases []internalconfig.OAuthModelAlias) []internalconfig.OAuthModelAlias {
	if len(aliases) == 0 {
		return nil
	}
	cfg := internalconfig.Config{
		OAuthModelAlias: map[string][]internalconfig.OAuthModelAlias{
			"auth": aliases,
		},
	}
	cfg.SanitizeOAuthModelAlias()
	clean := cfg.OAuthModelAlias["auth"]
	if len(clean) == 0 {
		return nil
	}
	return append([]internalconfig.OAuthModelAlias(nil), clean...)
}

func resolveUpstreamModelFromAliases(aliases []internalconfig.OAuthModelAlias, requestedModel string) OAuthModelAliasResult {
	if len(aliases) == 0 {
		return OAuthModelAliasResult{}
	}
	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if len(candidates) == 0 {
		return OAuthModelAliasResult{}
	}
	baseModel := requestResult.ModelName
	if baseModel == "" {
		baseModel = strings.TrimSpace(requestedModel)
	}
	for _, candidate := range candidates {
		key := strings.TrimSpace(candidate)
		if key == "" {
			continue
		}
		for _, entry := range aliases {
			original := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if original == "" || alias == "" || !strings.EqualFold(alias, key) {
				continue
			}
			if strings.EqualFold(original, baseModel) {
				if !entry.ForceMapping {
					return OAuthModelAliasResult{}
				}
				return OAuthModelAliasResult{
					UpstreamModel: preserveResolvedModelSuffix(original, requestResult),
					ForceMapping:  entry.ForceMapping,
					OriginalAlias: oauthModelAliasForceMappingResponseModel(alias),
				}
			}
			originalAlias := requestedModel
			if entry.ForceMapping {
				originalAlias = oauthModelAliasForceMappingResponseModel(alias)
			}
			return OAuthModelAliasResult{
				UpstreamModel: preserveResolvedModelSuffix(original, requestResult),
				ForceMapping:  entry.ForceMapping,
				OriginalAlias: originalAlias,
			}
		}
	}
	return OAuthModelAliasResult{}
}

func (m *Manager) applyOAuthModelAliasWithResult(auth *Auth, requestedModel string) OAuthModelAliasResult {
	result := m.resolveOAuthModelAliasWithResult(auth, requestedModel)
	if result.UpstreamModel == "" {
		return OAuthModelAliasResult{UpstreamModel: requestedModel}
	}
	return result
}

func resolveUpstreamModelFromAliasTable(m *Manager, auth *Auth, requestedModel, channel string) OAuthModelAliasResult {
	if m == nil || auth == nil {
		return OAuthModelAliasResult{}
	}
	if channel == "" {
		return OAuthModelAliasResult{}
	}

	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return OAuthModelAliasResult{}
	}

	// Use the snapshot-local cache for the auth-independent phases
	// (force-mapping, upstream-model match, alias match). The auth-specific
	// registry lookup runs live between the force-mapping phase and the
	// tail phases so it is never served from cache.
	cached := table.resolveTableOnly(channel, requestedModel)
	if cached.forceMappingHit {
		return cached.forceResult
	}

	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if resolved := resolveRequestedModelForAuth(m, auth, channel, candidates, requestResult); strings.TrimSpace(resolved) != "" {
		return OAuthModelAliasResult{UpstreamModel: resolved}
	}

	return cached.tailResult
}

// resolveUpstreamModelFromAliasTableStateKey is the state-key variant of
// resolveUpstreamModelFromAliasTable: a configured (non-force) alias target
// always outranks the auth-specific registry lookup, because state keys must
// follow the configured alias→target mapping for Reconcile/MarkResult state
// migration regardless of what the registry advertises under the alias name.
func resolveUpstreamModelFromAliasTableStateKey(m *Manager, auth *Auth, requestedModel, channel string) OAuthModelAliasResult {
	if m == nil || auth == nil {
		return OAuthModelAliasResult{}
	}
	if channel == "" {
		return OAuthModelAliasResult{}
	}

	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return OAuthModelAliasResult{}
	}

	cached := table.resolveTableOnly(channel, requestedModel)
	if cached.forceMappingHit {
		return cached.forceResult
	}
	if cached.tailResult.UpstreamModel != "" {
		return cached.tailResult
	}

	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if resolved := resolveRequestedModelForAuth(m, auth, channel, candidates, requestResult); strings.TrimSpace(resolved) != "" {
		return OAuthModelAliasResult{UpstreamModel: resolved}
	}

	return cached.tailResult
}

// resolveTableOnly returns the memoized auth-independent alias table
// resolution for (channel, requestedModel). The result is cached on the
// snapshot, so config reloads (which publish a new table pointer) auto-
// matically invalidate it. Negative results are cached as zero-value
// tailResult entries; entries are never stored as nil pointers.
func (t *oauthModelAliasTable) resolveTableOnly(channel, requestedModel string) oauthAliasCacheValue {
	key := oauthAliasCacheKey{
		channel:        strings.ToLower(strings.TrimSpace(channel)),
		requestedModel: strings.TrimSpace(requestedModel),
	}
	t.cacheMu.RLock()
	value, ok := t.cache[key]
	t.cacheMu.RUnlock()
	if ok {
		return value
	}
	value = t.computeTableOnly(channel, requestedModel)
	t.cacheMu.Lock()
	if t.cache == nil {
		t.cache = make(map[oauthAliasCacheKey]oauthAliasCacheValue)
	}
	if _, exists := t.cache[key]; !exists {
		if len(t.cache) >= oauthAliasCacheMaxEntries {
			// The table is an immutable snapshot, so clearing only affects the
			// optimization and cannot change alias resolution semantics.
			clear(t.cache)
		}
		t.cache[key] = value
		t.cacheComputes.Add(1)
	}
	t.cacheMu.Unlock()
	return value
}

// computeTableOnly evaluates the auth-independent phases of the alias table
// resolution: force-mapping aliases first, then the "requested model is an
// upstream model" phase (precomputed upstreamIndex set lookup), then plain
// alias resolution. The auth-specific registry lookup is intentionally
// excluded; the caller runs it between the force-mapping phase and the
// tail phases so its result never pollutes the cache.
func (t *oauthModelAliasTable) computeTableOnly(channel, requestedModel string) oauthAliasCacheValue {
	rev := t.reverse[channel]
	if len(rev) == 0 {
		return oauthAliasCacheValue{}
	}
	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if len(candidates) == 0 {
		return oauthAliasCacheValue{}
	}
	baseModel := requestResult.ModelName

	// Phase 0: force-mapping aliases win before the auth-specific registry path.
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		entry, exists := rev[key]
		if !exists || !entry.forceMapping {
			continue
		}
		targetModel := strings.TrimSpace(entry.upstreamModel)
		if targetModel == "" {
			continue
		}
		return oauthAliasCacheValue{
			forceMappingHit: true,
			forceResult: OAuthModelAliasResult{
				UpstreamModel: preserveResolvedModelSuffix(targetModel, requestResult),
				ForceMapping:  entry.forceMapping,
				OriginalAlias: oauthModelAliasForceMappingResponseModel(entry.configAlias),
			},
		}
	}

	// Phase 1: requested model is itself a configured upstream model
	// (precomputed index lookup instead of a per-request scan).
	upstreamIdx := t.upstreamIndex[channel]
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		if _, ok := upstreamIdx[key]; ok {
			return oauthAliasCacheValue{
				tailResult: OAuthModelAliasResult{UpstreamModel: preserveResolvedModelSuffix(candidate, requestResult)},
			}
		}
	}

	// Phase 2: requested model is a configured alias.
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		entry, exists := rev[key]
		if !exists {
			continue
		}

		targetModel := entry.upstreamModel
		if targetModel == "" {
			continue
		}

		if strings.EqualFold(targetModel, baseModel) {
			if !entry.forceMapping {
				return oauthAliasCacheValue{}
			}
			return oauthAliasCacheValue{
				tailResult: OAuthModelAliasResult{
					UpstreamModel: preserveResolvedModelSuffix(targetModel, requestResult),
					ForceMapping:  entry.forceMapping,
					OriginalAlias: oauthModelAliasForceMappingResponseModel(entry.configAlias),
				},
			}
		}

		var upstreamModel string
		if thinking.ParseSuffix(targetModel).HasSuffix {
			upstreamModel = targetModel
		} else if requestResult.HasSuffix && requestResult.RawSuffix != "" {
			upstreamModel = targetModel + "(" + requestResult.RawSuffix + ")"
		} else {
			upstreamModel = targetModel
		}

		originalAlias := requestedModel
		if entry.forceMapping {
			originalAlias = oauthModelAliasForceMappingResponseModel(entry.configAlias)
		}
		return oauthAliasCacheValue{
			tailFork: entry.fork,
			tailResult: OAuthModelAliasResult{
				UpstreamModel: upstreamModel,
				ForceMapping:  entry.forceMapping,
				OriginalAlias: originalAlias,
			},
		}
	}

	return oauthAliasCacheValue{}
}

// tableOnlyComputeCount reports how many table-only alias resolutions were
// actually computed (cache misses) for this snapshot. Tests use it to verify
// memoization reuse without injecting test-only hooks into the hot path.
func (t *oauthModelAliasTable) tableOnlyComputeCount() int64 {
	if t == nil {
		return 0
	}
	return t.cacheComputes.Load()
}

func resolveRequestedModelForAuth(m *Manager, auth *Auth, channel string, candidates []string, requestResult thinking.SuffixResult) string {
	if auth == nil || len(candidates) == 0 {
		return ""
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return ""
	}
	reg := registry.GetGlobalRegistry()
	if reg == nil {
		return ""
	}
	models := reg.GetModelsForClient(authID)
	if len(models) == 0 {
		return ""
	}
	for _, candidate := range candidates {
		modelKey := canonicalModelKey(candidate)
		if modelKey == "" {
			continue
		}
		var aliasResolved string
		for _, model := range models {
			if model == nil || !strings.EqualFold(strings.TrimSpace(model.ID), modelKey) {
				continue
			}
			execTarget := strings.TrimSpace(model.ExecutionTarget)
			if execTarget != "" {
				// Alias-exposed model: resolve to upstream execution target.
				aliasResolved = preserveResolvedModelSuffix(execTarget, requestResult)
				break
			}
		}
		if aliasResolved != "" {
			log.Debugf("[DEBUG] resolveUpstreamModelFromAliasTable: candidate %s is alias-exposed by auth %s, executing upstream %s", candidate, auth.ID, aliasResolved)
			return aliasResolved
		}
	}
	return ""
}

func configuredAliasTargetForCandidate(m *Manager, channel, candidate string, requestResult thinking.SuffixResult) string {
	if m == nil {
		return ""
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(candidate))
	if key == "" {
		return ""
	}
	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return ""
	}
	entry, ok := table.reverse[channel][key]
	if !ok || !entry.fork {
		return ""
	}
	original := strings.TrimSpace(entry.upstreamModel)
	if original == "" || strings.EqualFold(original, candidate) {
		return ""
	}
	return preserveResolvedModelSuffix(original, requestResult)
}

func (m *Manager) resolveBlockedForkAliasTarget(auth *Auth, requestedModel string) string {
	if m == nil || auth == nil {
		return ""
	}
	channel := modelAliasChannel(auth)
	if channel == "" {
		return ""
	}
	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return ""
	}
	reverse := table.reverse[channel]
	if len(reverse) == 0 {
		return ""
	}
	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		entry, ok := reverse[key]
		if key == "" || !ok || !entry.fork {
			continue
		}
		original := strings.TrimSpace(entry.upstreamModel)
		if original == "" {
			continue
		}
		return preserveResolvedModelSuffix(original, requestResult)
	}
	return ""
}

// modelAliasChannel extracts the OAuth model alias channel from an Auth object.
// It determines the provider and auth kind from the Auth's attributes and delegates
// to OAuthModelAliasChannel for the actual channel resolution.
func modelAliasChannel(auth *Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	authKind := auth.AuthKind()
	return OAuthModelAliasChannel(provider, authKind)
}

// OAuthModelAliasChannel returns the OAuth model alias channel name for a given provider
// and auth kind. Returns empty string if the provider/authKind combination doesn't support
// OAuth model alias (e.g., API key authentication).
//
// Supported channels: gemini-cli, vertex, aistudio, antigravity, claude, codex, iflow, kiro, github-copilot, kimi, xai.

// Built-in channels: gemini-cli, vertex, aistudio, antigravity, claude, codex, kimi.
// Plugin OAuth providers use their normalized provider key as the channel.

func OAuthModelAliasChannel(provider, authKind string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authKind = normalizeOAuthModelAliasAuthKind(authKind)
	if authKind == "apikey" {
		return ""
	}
	switch provider {
	case "gemini":
		// gemini provider uses gemini-api-key config, not oauth-model-alias.
		// OAuth-based gemini auth is converted to "gemini-cli" by the synthesizer.
		return ""
	case "vertex":
		return "vertex"
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "gemini-cli", "aistudio", "antigravity", "iflow", "kiro", "github-copilot", "kimi", "xai":
		return provider
	default:
		return provider
	}
}

func normalizeOAuthModelAliasAuthKind(authKind string) string {
	authKind = strings.ToLower(strings.TrimSpace(authKind))
	switch authKind {
	case "api_key", "api-key":
		return "apikey"
	default:
		return authKind
	}
}
