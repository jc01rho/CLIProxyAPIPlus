---
name: cli-proxy-release
description: Manage CLIProxyAPIPlus, Cli-Proxy-API-Management-Center, and cpa-usage-keeper releases under ~/git/cli-proxy. Merges upstream, bumps version tags, and pushes. Use when the user asks to merge upstream, bump version, tag release, or release any cli-proxy sub-project.
---

# CLI Proxy Release Skill

## Scope

This skill ONLY operates within `~/git/cli-proxy`. Do not run outside this path.

## Sub-projects

| Directory | Upstream | Tag Pattern |
|---|---|---|
| `CLIProxyAPIPlus` | `https://github.com/router-for-me/CLIProxyAPI.git` (upstream) | `v<major>.<minor>.<patch>-<seq>` |
| `Cli-Proxy-API-Management-Center` | `https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git` (upstream) | `v<major>.<minor>.<patch>-<seq>` |
| `cpa-usage-keeper` | `https://github.com/Willxup/cpa-usage-keeper.git` (upstream) | `v<major>.<minor>.<patch>-<seq>` |

## Version Tag Format

Tags follow the pattern: `v<major>.<minor>.<patch>-<seq>`

### Rules

1. **Suffix-only increment**: For follow-up releases on the same base version, increment only the suffix.
   - If latest tag is `v7.1.32`, next tag should be `v7.1.32-1`
   - Then `v7.1.32-2`, `v7.1.32-3`, etc.
   - If upstream publishes `v7.1.33`, reset: `v7.1.33-1`

2. **Check upstream latest BASE version first** (MANDATORY before merge):
   - Fetch upstream: `git fetch upstream --prune` (tags via `git ls-remote --tags upstream`, not `git fetch --tags`)
   - Find latest upstream BASE tag (no suffix): `git ls-remote --tags upstream | awk '{print $2}' | sed 's|refs/tags/||' | grep -v '\^' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1`
      - This extracts the highest `v<major>.<minor>.<patch>` (no `-N` suffix) using exact semver pattern
   - Compare upstream BASE version with local latest tag's BASE version
   - If upstream base > local base: new tag = `<upstream_base>-1`
   - If upstream base == local base: new tag = `<local_base>-<local_suffix+1>`
   - If upstream base < local base (shouldn't happen): new tag = `<local_base>-<local_suffix+1>`

3. **Algorithm** (implemented as bash script):
   ```bash
   version_gt() {
     [ "$1" = "$2" ] && return 1
     printf '%s\n%s\n' "$2" "$1" | sort -V -C
   }

   # Get upstream latest BASE version (no suffix like -1, -2)
   upstream_base=$(git ls-remote --tags upstream | grep 'refs/tags/' | grep -v '\^{}' | sed 's|.*/||' | grep -v '-[0-9]$' | sort -V | tail -1)

   # Get local latest tag
   local_latest=$(git describe --tags --abbrev=0 2>/dev/null || echo "")

   if [ -z "$local_latest" ]; then
     new_tag="${upstream_base}-1"
   else
     if [[ "$local_latest" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$ ]]; then
       local_base="${local_latest%-*}"
       local_suffix="${local_latest##*-}"
     else
       local_base="$local_latest"
       local_suffix=0
     fi

     if version_gt "$upstream_base" "$local_base"; then
       new_tag="${upstream_base}-1"
     else
       new_tag="${local_base}-$((local_suffix + 1))"
     fi
   fi
   ```

## Release Process (CRITICAL: upstream check comes BEFORE local check)

### Step 0: Fetch upstream for ALL subdirectories FIRST (MANDATORY)

Before analyzing any single project, fetch upstream commits AND tags for every subdirectory in a single batch. This guarantees that the per-project decisions below (upstream has new commits? base version changed?) are based on a fully synchronized remote state across the whole monorepo.

```bash
PROJECTS=(CLIProxyAPIPlus Cli-Proxy-API-Management-Center cpa-usage-keeper)
for p in "${PROJECTS[@]}"; do
  cd ~/git/cli-proxy/"$p" || continue
  if git remote get-url upstream >/dev/null 2>&1; then
    # Fetch commits AND tags, prune deleted remote refs.
    # Tags are mandatory because the version algorithm below compares
    # upstream's latest base tag (e.g. v7.1.32) against the local latest tag.
    # Commits/branches only — do NOT use --tags here: fork tags often
    # "would clobber existing tag" and make fetch exit 1 even when main moved.
    # Upstream tag versions are read via `git ls-remote --tags upstream` in Step 4.
    git fetch upstream --prune
  else
    echo "[$p] no upstream remote — skip fetch"
  fi
done
```

If any `git fetch upstream --prune` fails, STOP and report which project failed. Do not proceed to per-project analysis with a stale remote state.

Only after this batched fetch succeeds, process each subdirectory in the order below.

### Step 1: Fetch upstream (per-project safety net)

The batched Step 0 above is the canonical fetch. This per-project step is a safety net in case Step 0 was skipped (e.g. running the skill against a single project). Use `git fetch upstream --prune` only; read upstream tags with `git ls-remote --tags upstream`.

```bash
if git remote get-url upstream >/dev/null 2>&1; then
  git fetch upstream --prune
fi
```

### Step 2: Check upstream for new commits (BEFORE local check)

```bash
upstream_new_commits=$(git log HEAD..upstream/main --oneline 2>/dev/null)
```

- **If upstream has new commits → MERGE FIRST** (see Merge Strategy below), then continue to step 3.
- **If upstream has no new commits → skip merge, continue to step 3.**

### Step 3: Check for new commits since latest tag (local, AFTER merge)

```bash
latest_tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
if [ -n "$latest_tag" ]; then
  new_commits=$(git log ${latest_tag}..HEAD --oneline)
else
  new_commits=$(git log --oneline)
fi
```

- **If `new_commits` is empty AND no merge happened in step 2 → SKIP bump.** Report "no new commits locally or from upstream, skipped" and move to next project.
- **If `new_commits` has content OR a merge happened in step 2 → proceed to bump.**

### Step 4: Determine next tag

Use the algorithm above. Key scenarios:
- Upstream merge brought new base version → `<upstream_base>-1`
- Same base, suffix increment → `<local_base>-<local_suffix+1>`

### Step 5: Create and push

```bash
git tag <new_tag>
git push origin main
git push origin <new_tag>
```

### Step 6: Report

Report each project: previous tag → new tag (or "skipped" with reason).

## Decision Matrix

| Local new commits | Upstream new commits | Action |
|---|---|---|
| Yes | Yes | Merge upstream → tag → push |
| Yes | No | Tag → push |
| No | Yes | Merge upstream → tag → push |
| No | No | **Skip** — report "no new commits" |

**Key insight**: Even if there are no local commits since the last tag, upstream may have new commits that need to be merged and tagged. Always check upstream FIRST.

## Merge Strategy: Preserve Local Modified Features

### Pre-merge: Stash Uncommitted Local Changes

```bash
# Check for uncommitted changes
if ! git diff --quiet || ! git diff --cached --quiet; then
  git stash push -m "pre-upstream-merge-$(date +%Y%m%d%H%M%S)"
  STASHED=true
fi
```

### Merge with Conflict Detection

```bash
git merge upstream/main --no-edit
```

- **If merge succeeds (no conflicts)** → proceed to post-merge steps.
- **If merge conflicts detected** → resolve with local-preservation strategy (see below).

### Conflict Resolution: Apply Upstream, Keep Local Modifications

**Resolution approach by conflict type:**

1. **modify/delete conflicts** (local deleted, upstream modified):
   - Keep the local deletion (local intentionally removed the file)
   - `git rm <file>` to confirm deletion

2. **content conflicts** (both sides modified the same region):
   - **Keep both sides' changes** — include local modifications AND upstream changes
   - For code: merge local features into upstream structure
   - Prefer upstream structure as base, embed local-only features into it

3. **import/type conflicts** (local added imports, upstream changed types):
   - Include all imports from both sides
   - Use upstream's type definitions

**Steps:**
```bash
# 1. Identify conflicts
git diff --name-only --diff-filter=U

# 2. For each conflict file, resolve manually:
#    - Read both sides of the conflict
#    - Apply upstream changes as the base structure
#    - Embed local-only modifications into the upstream structure
#    - Remove all conflict markers (<<<<<<< HEAD, =======, >>>>>>> upstream/main)

# 3. Verify no conflict markers remain
grep -rn "<<<<<<< HEAD" . --include="*.go" --include="*.md" --include="*.ts" --include="*.yml"

# 4. Verify build (for Go projects)
go build ./...

# 5. Stage and commit
git add -A
git commit --no-edit
```

### Post-merge: Restore Stashed Changes

```bash
if [ "$STASHED" = "true" ]; then
  git stash pop
  if [ $? -ne 0 ]; then
    echo "WARNING: Stash pop caused conflicts. Resolve manually."
    git diff --name-only --diff-filter=U
  fi
fi
```

### Post-merge Cleanup: Remove Sponsor Section (MANDATORY for CLIProxyAPIPlus)

```bash
for f in README.md README_CN.md README_JA.md; do
  if [ -f "$f" ]; then
    sed -i '/^## Sponsor$/,/^## [^S]/{ /^## [^S]/!d }' "$f"
  fi
done
```

### Key Principles

1. **Never use `--force` or `--theirs`** on merge — local features must not be silently overwritten
2. **Always check for conflicts** before committing the merge
3. **Stash before merge** — uncommitted local work must survive the merge
4. **Apply upstream, preserve local** — accept upstream improvements, keep local-only modifications
5. **Upstream structure as base** — when refactoring conflicts arise, prefer upstream's structure and embed local features into it

## Post-merge Route Registration Guard (MANDATORY for CLIProxyAPIPlus)

After every `git merge upstream/main` that touches `internal/api/server.go`, run the following verification BEFORE tagging.

### Verification Script

```bash
cd ~/git/cli-proxy/CLIProxyAPIPlus

# 1. Get all exported handler methods with (c *gin.Context) parameter
grep -rn '^func (h \*Handler) [A-Z]' internal/api/handlers/management/ --include='*.go' -h \
  | grep 'c \*gin.Context' \
  | sed -E 's/.*func \(h \*Handler\) ([A-Z][A-Za-z]+)\(c.*/\1/' | sort -u > /tmp/existing_handlers.txt

# 2. Get all registered handler names in server.go
awk '/^func \(s \*Server\) registerManagementRoutes/,/^}$/' internal/api/server.go \
  | grep -oE 's\.mgmt\.[A-Z][A-Za-z]+' | sed 's/s\.mgmt\.//' | sort -u > /tmp/registered_handlers.txt

# 3. Find missing routes
MISSING=$(comm -23 /tmp/existing_handlers.txt /tmp/registered_handlers.txt)
if [ -n "$MISSING" ]; then
  echo "ERROR: The following handlers are NOT registered in server.go:"
  echo "$MISSING"
  exit 1
fi
echo "OK: All handlers are registered."
```

### Post-merge Build Verification (MANDATORY)

After merging upstream into CLIProxyAPIPlus:

```bash
cd ~/git/cli-proxy/CLIProxyAPIPlus
go build ./...
```

If build fails, fix all errors before tagging. Common post-merge issues:
- Missing local-only functions (restore from pre-merge commit)
- Deleted upstream symbols still referenced locally (update references)
- Conflict marker residue in source files

## Important

- **Always check upstream FIRST**, then local — this is the #1 fix from the previous version
- **Bump whenever there are new commits**: local-only, upstream-only, or both → always bump
- **Skip only if NO new commits from either source**
- Always push the tag to origin after creating it
- Report each project's previous tag → new tag clearly (or "skipped" with reason)
- Never create release tags from the repository root — always inside the relevant subdirectory
- If merge conflicts occur, resolve them using local-preservation strategy — do not force merge
