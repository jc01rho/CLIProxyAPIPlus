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

   # Get upstream latest BASE version (no suffix like -1, -2).
   # Match exact semver: `grep -v '-[0-9]$'` only strips single-digit suffixes,
   # so a fork tag like v7.2.131-10 would survive and be misread as a base.
   upstream_base=$(git ls-remote --tags upstream | awk '{print $2}' | sed 's|refs/tags/||' | grep -v '\^' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)

   # Get local latest tag.
   # NEVER use `git describe --tags --abbrev=0` here: it returns the nearest tag
   # reachable from HEAD, so a freshly fetched upstream base tag that is not yet
   # an ancestor of HEAD is invisible. That silently reuses an already-published
   # suffix and the push is rejected. Sort the whole tag list by version instead.
   local_latest=$(git tag --sort=-v:refname | head -1)

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

### Step 0a: Land active worktree branches (MANDATORY — runs before the fetch)

Work done in a `*.gajae-code-worktrees/*` checkout lives on a side branch. That
branch is invisible to every check below, which all run against `main` in the
primary checkout. Releasing without landing it tags a tree that does not contain
the work you just finished, and the branch silently misses the release.

```bash
PROJECTS=(CLIProxyAPIPlus Cli-Proxy-API-Management-Center cpa-usage-keeper)
for p in "${PROJECTS[@]}"; do
  cd ~/git/cli-proxy/"$p" || continue
  git worktree list | tail -n +2 | while read -r wt_path wt_sha wt_branch; do
    branch=$(printf '%s' "$wt_branch" | tr -d '[]')
    ahead=$(git rev-list --count "main..$branch" 2>/dev/null || echo 0)
    dirty=$(git -C "$wt_path" status --porcelain | wc -l)
    printf '[%s] worktree %s branch=%s ahead_of_main=%s uncommitted=%s\n' \
      "$p" "$wt_path" "$branch" "$ahead" "$dirty"
  done
done
```

For every worktree branch reported with `ahead_of_main > 0`:

1. Commit or explicitly discard uncommitted work in that worktree. Never tag
   with `uncommitted > 0` — the release would omit it.
2. Merge the branch into `main` in the primary checkout: `git merge <branch> --no-edit`.
3. If the primary checkout refuses with "local changes would be overwritten",
   inspect `git diff` there before doing anything. Stray edits in the primary
   checkout are usually a duplicate of work already committed in the worktree;
   stash, merge, then pop and reconcile. Never discard without reading the diff.

Only after every worktree branch is landed (or confirmed intentionally
unreleased) proceed to the upstream fetch.

**Worktree branch created before an upstream merge**: after merging upstream into
`main`, sync the worktree with `git merge main --no-edit` inside the worktree
before running its tests. Otherwise the branch is validated against a pre-merge
tree and the post-merge result is never actually tested.

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
latest_tag=$(git tag --sort=-v:refname | head -1)
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
- **If merge conflicts detected** → classify every conflict before editing.

### Mandatory User Decision for Feature Conflicts

If both the local branch and upstream contain meaningful behavior in a conflicting
area, stop and ask the user which behavior to keep before resolving the conflict.
This applies to feature logic, API or wire behavior, data semantics, configuration,
UI behavior, security policy, migration behavior, and tests that encode different
intended outcomes.

- Do not silently choose `ours`, `theirs`, or a synthesized behavior.
- Do not infer that a passing build means the feature conflict is resolved.
- Do not commit, tag, or push while a feature-conflict decision is outstanding.
- Report the local behavior, upstream behavior, affected files, and concrete
  resolution choices.
- Resume only after the user explicitly selects a resolution.

Purely mechanical conflicts that preserve identical behavior, such as import
ordering, generated lockfile convergence, code moved by an upstream refactor, or
an upstream edit to a section the fork mandatorily deletes (the Sponsor block),
may be resolved without asking. Record why the resolution is behavior-neutral and
prove it — for the Sponsor case, `git diff HEAD -- README*.md` must come back
empty.

After the user decides a feature conflict, apply the local-preservation strategy
below using that decision as the binding resolution.

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

The localized headings are `## Sponsor`, `## 赞助商`, and `## スポンサー`. An
English-only pattern silently leaves the CN and JA sponsor blocks in the fork.

```bash
for f in README.md README_CN.md README_JA.md; do
  [ -f "$f" ] || continue
  # Drop from the sponsor heading up to (not including) the next `## ` heading.
  # Do not use `sed '/^## Sponsor$/,/^## [^S]/'`: that range ends on the next
  # heading whose first letter is not "S", so it also eats a following section
  # such as `## Supported Providers`, and it never matches the CN/JA headings.
  awk '
    /^## / {
      if ($0 ~ /^## (Sponsor|赞助商|スポンサー)[[:space:]]*$/) { skip = 1; next }
      skip = 0
    }
    !skip
  ' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
done
```

Verify all three languages, not just English. The old check grepped only
`## Sponsor`, so a surviving `## 赞助商` block still reported clean:

```bash
grep -n '^## \(Sponsor\|赞助商\|スポンサー\)' README.md README_CN.md README_JA.md
# must print nothing
grep -ln 'PackyCode\|packyapi\|FennoAI\|RunAPI\|CyberPay' README.md README_CN.md README_JA.md
# must print nothing — catches sponsor bodies left behind by a broken heading match
```

**Sponsor-only upstream commits still conflict.** When upstream edits the sponsor
block, the merge conflicts against the fork's deletion in all three READMEs. The
HEAD side is empty and the upstream side re-adds the block, so keeping HEAD is
the correct behavior-neutral resolution and needs no user decision. Prove it
afterwards — `git diff HEAD -- README*.md` must be empty, meaning the fork's
READMEs are byte-identical to their pre-merge state:

```bash
for f in README.md README_CN.md README_JA.md; do
  perl -0pi -e 's/^<<<<<<< HEAD\n(.*?)^=======\n.*?^>>>>>>> upstream\/main\n/$1/gms' "$f"
done
grep -n '^<<<<<<< \|^=======$\|^>>>>>>> ' README.md README_CN.md README_JA.md   # must be empty
git diff HEAD --stat -- README.md README_CN.md README_JA.md                     # must be empty
```

### Key Principles

1. **Never use `--force` or `--theirs`** on merge — local features must not be silently overwritten
2. **Always check for conflicts** before committing the merge
3. **Stash before merge** — uncommitted local work must survive the merge
4. **Apply upstream, preserve local** — accept upstream improvements, keep local-only modifications
5. **Upstream structure as base** — when refactoring conflicts arise, prefer upstream's structure and embed local features into it

## Post-merge Route Registration Guard (MANDATORY for CLIProxyAPIPlus)

Run this verification BEFORE tagging after every `git merge upstream/main` that
touches anything under `internal/api/`. Do not scope the trigger to a single
file: upstream relocates route registration between files (it has already moved
`registerManagementRoutes` out of `server.go` into `server_management.go`), so a
filename-scoped trigger skips the check exactly when it matters most.

### Verification Script

```bash
cd ~/git/cli-proxy/CLIProxyAPIPlus

# 1. Get all exported handler methods with (c *gin.Context) parameter
grep -rn '^func (h \*Handler) [A-Z]' internal/api/handlers/management/ --include='*.go' -h \
  | grep 'c \*gin.Context' \
  | sed -E 's/.*func \(h \*Handler\) ([A-Z][A-Za-z]+)\(c.*/\1/' | sort -u > /tmp/existing_handlers.txt

# 2. Get all registered handler names.
# Scan every file in internal/api/ rather than a hardcoded server.go with a
# function-range awk: upstream moves registerManagementRoutes between files and
# splits registration across helpers. A path- or function-scoped scan silently
# matches nothing and then reports every handler as unregistered.
grep -rhoE 's\.mgmt\.[A-Z][A-Za-z]+' --include='*.go' internal/api/ \
  | sed 's/s\.mgmt\.//' | sort -u > /tmp/registered_handlers.txt

# 3. Self-check the scan before trusting its verdict.
# An empty registration set means the scan itself broke, not that every route
# vanished. Without this guard a stale scan produces ~142 false positives that
# look like a catastrophic regression and train you to ignore the guard.
if [ ! -s /tmp/registered_handlers.txt ]; then
  echo "ERROR: guard found no registered handlers at all — the scan is broken, not the routes."
  echo "Locate the current registration site and fix this script:"
  grep -rn 'func (s \*Server) registerManagementRoutes' internal/api/
  exit 1
fi

# 4. Find missing routes
MISSING=$(comm -23 /tmp/existing_handlers.txt /tmp/registered_handlers.txt)
if [ -n "$MISSING" ]; then
  echo "ERROR: The following handlers are NOT registered:"
  echo "$MISSING"
  exit 1
fi
echo "OK: All $(wc -l < /tmp/existing_handlers.txt) handlers are registered."

# 5. Duplicate route registration check (upstream merges have introduced these).
# Match the receiver too (`v1.GET(...)`, not bare `GET(...)`): different route
# groups legitimately share a relative path — v1 and v1beta both expose
# GET("/models"), v1 and codexDirect both expose POST("/responses"). A
# receiver-blind check reports ~7 false positives on a healthy tree, and a guard
# that always warns is a guard everyone ignores. Exclude tests: they register
# throwaway routes on their own engines.
DUPES=$(grep -rhoE '\b[a-zA-Z_][a-zA-Z0-9_]*\.(GET|POST|PUT|PATCH|DELETE)\("[^"]+"' \
  --include='*.go' --exclude='*_test.go' internal/api/ | sort | uniq -d)
if [ -n "$DUPES" ]; then
  echo "WARNING: duplicate group+path registrations:"
  echo "$DUPES"
else
  echo "OK: no duplicate route registrations."
fi
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

### Distinguishing New Breakage From Pre-existing Failures (MANDATORY)

Some packages carry failing tests that predate your work. Never claim a failure
is "pre-existing" from memory or vibes — prove it by diffing against the same
tree without your changes:

```bash
go test ./... -count=1 2>&1 | grep -E "^--- FAIL" | sort -u > /tmp/after.txt
git stash -q
go test ./... -count=1 2>&1 | grep -E "^--- FAIL" | sort -u > /tmp/before.txt
git stash pop -q
diff /tmp/before.txt /tmp/after.txt && echo "IDENTICAL — no new breakage"
```

Any line appearing only in `after.txt` is a regression you introduced and blocks
the tag. Report the pre-existing failure count explicitly rather than silently
tolerating a red suite.

Apply the same rule to `gofmt -l`: check whether a listed file was already
unformatted before your edit. Do not reformat files your change did not touch.

### Local-only Feature Preservation Check (MANDATORY for CLIProxyAPIPlus)

`go test ./sdk/cliproxy/auth/` is necessary but NOT sufficient — it only encodes
tested behavior, so untested local-only features get dropped while the suite
stays green. Also run a symbol-level diff against known-good commit `4ccff390`:

```bash
git show 4ccff390:sdk/cliproxy/auth/conductor.go \
  | grep -oE '^func [^{]+' | sed -E 's/^func (\([^)]*\) )?([A-Za-z0-9_]+).*/\2/' | sort -u > /tmp/old_funcs.txt
cat sdk/cliproxy/auth/conductor*.go \
  | grep -oE '^func [^{]+' | sed -E 's/^func (\([^)]*\) )?([A-Za-z0-9_]+).*/\2/' | sort -u > /tmp/new_funcs.txt
comm -23 /tmp/old_funcs.txt /tmp/new_funcs.txt
```

A reported symbol is NOT automatically a regression. Before restoring anything,
establish when it disappeared — it may have been refactored away by an earlier
local commit rather than dropped by this merge:

```bash
for f in $(comm -23 /tmp/old_funcs.txt /tmp/new_funcs.txt); do
  # Was it present in the pre-merge tree? Replace <pre-merge-sha> accordingly.
  printf '%s present_before_merge=%s\n' "$f" \
    "$(git grep -c "func .*\b$f\b" <pre-merge-sha> -- sdk/cliproxy/auth/ 2>/dev/null | wc -l)"
done
```

`present_before_merge=0` means the merge did not drop it. In that case verify the
underlying capability still exists under its current symbol names (threshold
routing, billing class, failover budget, alias-pool pinning, weighted-robin)
before concluding anything. Restore from `4ccff390` only when the merge genuinely
removed a still-referenced local function. Never edit or delete the tests.

## Important

- **Always check upstream FIRST**, then local — this is the #1 fix from the previous version
- **Bump whenever there are new commits**: local-only, upstream-only, or both → always bump
- **Skip only if NO new commits from either source**
- Always push the tag to origin after creating it
- Report each project's previous tag → new tag clearly (or "skipped" with reason)
- Never create release tags from the repository root — always inside the relevant subdirectory
- If merge conflicts occur, resolve them using local-preservation strategy — do not force merge
- **Land worktree branches before anything else** — unlanded side-branch work is invisible to every check and silently misses the release
- **Never trust a guard that cannot detect its own breakage** — a scan returning "everything is broken" usually means the scan broke, not the code
- **Prove "pre-existing"** — diff failures against a stashed tree instead of asserting it
