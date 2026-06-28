---
description: Bump version tags for CLIProxyAPIPlus, Cli-Proxy-API-Management-Center, and cpa-usage-keeper
agent: sisyphus
subtask: true
---
Load the `cli-proxy-release` skill and execute the full release process for all sub-projects under `~/git/cli-proxy`.

## Execution Order (STRICT — upstream fetch FIRST, then upstream check BEFORE local check)

### Phase 0: Batch upstream fetch for ALL sub-projects FIRST

Before processing any single project, fetch upstream commits AND tags for every sub-project in one batch. This guarantees all per-project decisions below are based on a fully synchronized remote state.

```bash
PROJECTS=(CLIProxyAPIPlus Cli-Proxy-API-Management-Center cpa-usage-keeper)
for p in "${PROJECTS[@]}"; do
  cd ~/git/cli-proxy/"$p" || continue
  if git remote get-url upstream >/dev/null 2>&1; then
    git fetch upstream --prune
  else
    echo "[$p] no upstream remote — skip fetch"
  fi
done
```

If any fetch fails, STOP and report which project failed. Do not proceed with stale remote state.

### Phase 1: Per-project Check

For each sub-project (CLIProxyAPIPlus, Cli-Proxy-API-Management-Center, cpa-usage-keeper):

1. `cd ~/git/cli-proxy/<project>`
2. `git status --short` — if dirty, `git stash push -m "pre-bump-$(date +%Y%m%d%H%M%S)"` and set STASHED=true
3. `git log HEAD..upstream/main --oneline 2>/dev/null` → store as `upstream_new`
4. `git describe --tags --abbrev=0 2>/dev/null` → store as `latest_tag`
5. `git log ${latest_tag}..HEAD --oneline 2>/dev/null` → store as `local_new`

### Phase 2: Decision

| upstream_new | local_new | Action |
|---|---|---|
| has commits | any | **Merge upstream** → tag → push |
| empty | has commits | **Tag** → push |
| empty | empty | **Skip** — report "no new commits" |

**Key rule**: Upstream new commits trigger a full merge + tag cycle, even if local has zero new commits.

### Phase 3: Merge (only if upstream has new commits)

1. `git merge upstream/main --no-edit`
2. If conflicts:
   - `git diff --name-only --diff-filter=U` to list conflict files
   - Resolve each file: keep local-only features, accept upstream structure
   - Remove ALL conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`)
   - `grep -rn "<<<<<<< HEAD" . --include="*.go"` to verify clean
   - `go build ./...` to verify compilation
   - `git add -A && git commit --no-edit`
3. **Post-merge guard (CLIProxyAPIPlus only)**: Run route registration check — if `internal/api/server.go` or `internal/api/handlers/management/` was touched, verify all handlers are registered in `registerManagementRoutes()`
4. **Post-merge sponsor cleanup (CLIProxyAPIPlus only)**: Remove `## Sponsor` section from README files
5. If STASHED: `git stash pop` — if conflicts, resolve and report

### Phase 4: Tag & Push

1. Determine next tag using version algorithm (see cli-proxy-release skill)
2. `git tag <new_tag>`
3. `git push origin main`
4. `git push origin <new_tag>`

### Phase 5: Report

Report for each project: `<project>: <old_tag> → <new_tag>` or `<project>: skipped — no new commits`

## Important

- NEVER skip a project that has upstream new commits
- NEVER force merge or use `--theirs`
- NEVER create tags from repository root — always inside the subdirectory
- If merge conflicts cannot be resolved automatically, STOP and report — do not guess
- Local-only features must NEVER be silently overwritten by upstream
