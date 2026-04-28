---
description: "Create a release from a release branch (e.g., 0.6.0-rc2 -> 0.6.0 final, or new RC)"
---

Create a release of mcp-gateway. Argument is the target version (e.g., `0.6.0`, `0.7.0-rc1`).

Version: $ARGUMENTS

If no version argument was provided, ask the user for it.

## Determine release type

Parse the version to determine:
- **RC release**: version contains `-rc` suffix (e.g., `0.6.0-rc1`)
- **Final release**: no suffix (e.g., `0.6.0`)
- **Patch release**: patch > 0 and no suffix (e.g., `0.6.1`)

Extract the major.minor.patch for the release branch name (`release-X.Y.Z`).

## Remote layout

This repo uses a fork workflow. Confirm with `git remote -v`:
- `origin` = user's fork (e.g., `<your-fork>/mcp-gateway`)
- `upstream` = Kuadrant/mcp-gateway

Both `main` and `release-*` branches have branch protection -- all changes require PRs.

## Important notes

- All changes go through PRs -- never push directly to protected branches
- The `set-release-version.sh` script handles all version string updates across docs, scripts, and manifests
- Creating the GitHub release triggers CI workflows that build images and push the Helm chart
- Workflow URLs to watch:
  - https://github.com/Kuadrant/mcp-gateway/actions/workflows/images.yaml
  - https://github.com/Kuadrant/mcp-gateway/actions/workflows/helm-release.yaml

## Safety rules

Follow these throughout the entire process:
- **Never push to any remote.** Always tell the user what to push and let them do it.
- **Never create PRs or issues.** Provide the exact `gh pr create` command for the user to run.
- **Never create GitHub releases.** Walk the user through it.
- **Check for uncommitted changes** before any `git checkout`. If the working tree is dirty, stop and ask the user how to proceed.
- **Show diffs and ask for confirmation** before committing. Never commit without the user confirming the changes look correct.
- **Never use `git add .` or `git add -A`.** Only stage specific paths relevant to the version bump.

## Steps

### 1. Pre-flight checks

Before doing anything:
1. Run `git status` to check for uncommitted changes. If dirty, stop and ask the user.
2. Run `git remote -v` to confirm the remote layout matches expectations.
3. Confirm the target version with the user before proceeding.

### 2. Sync and create a working branch

```bash
git fetch upstream
```

Check if the release branch exists on upstream:
```bash
git ls-remote --heads upstream release-X.Y.Z
```

**If it exists**, check it out and create the working branch:
```bash
git checkout release-X.Y.Z
git pull upstream release-X.Y.Z
git checkout -b release-{VERSION}
```

**If it does not exist** (new minor release), tell the user:

> The release branch `release-X.Y.Z` doesn't exist on upstream yet. Create it via the GitHub UI:
> 1. Go to https://github.com/Kuadrant/mcp-gateway
> 2. Click the branch dropdown, type `release-X.Y.Z`, and select **Create branch: release-X.Y.Z from main**
>
> Let me know when that's done and I'll continue.

Do NOT create the release branch yourself. Wait for the user to confirm before continuing.

### 3. Update version references

```bash
./scripts/set-release-version.sh {VERSION}
```

Show the diff with `git diff` and ask the user to confirm the version references look correct before proceeding.

### 4. Regenerate OLM bundle

If CRD or API type changes are included, run `make generate-all` first.

Then regenerate the bundle:
```bash
make bundle VERSION={VERSION}
```

### 5. Review and commit

Show the full diff of all changes with `git diff`. Present a summary of what changed and ask the user to confirm before committing.

Only after confirmation, stage and commit:
```bash
git add -u config/ charts/ docs/ bundle/ scripts/
git commit -s -m "Update version to {VERSION}"
```

### 6. Hand off to user for push and PR

STOP here. Do not push. Do not create a PR. Tell the user:

> Ready for you to push and open the PR. Run:
> ```
> git push -u origin release-{VERSION}
> ```
>
> Then create a PR targeting the `release-X.Y.Z` branch on Kuadrant/mcp-gateway:
> ```
> gh pr create --repo Kuadrant/mcp-gateway --base release-X.Y.Z \
>   --title "Update version to {VERSION}" \
>   --body "Version bump for {VERSION} release."
> ```

Wait for the user to confirm the PR is merged before proceeding to the next step.

### 7. Create GitHub release (after PR merges)

Tell the user to create the release:
1. Go to https://github.com/Kuadrant/mcp-gateway/releases
2. Click **Draft a new release**
3. Click **Choose a tag**, create tag `v{VERSION}`, target the `release-X.Y.Z` branch
4. Set title to `v{VERSION}`
5. Click **Generate release notes**
6. For RCs: check **Set as a pre-release**
7. For final releases: check **Set as the latest release**
8. Click **Publish release**

### 8. Provide verification command

Give the user this to run after workflows complete:

```bash
VERSION={VERSION}
for image in \
  ghcr.io/kuadrant/mcp-gateway:v${VERSION} \
  ghcr.io/kuadrant/mcp-controller:v${VERSION} \
  ghcr.io/kuadrant/mcp-controller-bundle:v${VERSION} \
  ghcr.io/kuadrant/mcp-controller-catalog:v${VERSION}; do
  docker manifest inspect "$image" > /dev/null 2>&1 \
    && echo "OK $image" || echo "MISSING $image"
done
helm show chart oci://ghcr.io/kuadrant/charts/mcp-gateway --version ${VERSION} > /dev/null 2>&1 \
  && echo "OK helm chart ${VERSION}" || echo "MISSING helm chart ${VERSION}"
```

### 9. Post-release: bump version on main (final releases only)

Skip this step for RC releases.

For final releases, main needs a version bump so docs and scripts reference the latest release.

```bash
git checkout main && git pull upstream main
git checkout -b bump-version-{VERSION}
./scripts/set-release-version.sh {VERSION}
make bundle VERSION={VERSION}
```

Show the diff and ask the user to confirm, then commit:
```bash
git add -u config/ charts/ docs/ bundle/ scripts/
git commit -s -m "Update version to {VERSION}"
```

Then tell the user to push and open a PR:

> Ready for you to push and open the main-branch version bump PR. Run:
> ```
> git push -u origin bump-version-{VERSION}
> ```
>
> Then:
> ```
> gh pr create --repo Kuadrant/mcp-gateway --base main \
>   --title "Bump version to {VERSION}" \
>   --body "Post-release version bump for {VERSION}."
> ```

