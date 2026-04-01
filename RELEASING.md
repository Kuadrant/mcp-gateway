# Releasing MCP Gateway

Creating a GitHub release with a `vX.Y.Z` tag triggers automated workflows that:
1. Build and push container images (`mcp-gateway`, `mcp-controller`) to `ghcr.io/kuadrant/`
2. Build and push OLM bundle and catalog images to `ghcr.io/kuadrant/`
3. Package and push the Helm chart to `oci://ghcr.io/kuadrant/charts/mcp-gateway`

## Release Steps

### 1. Create Release Branch and Update Version

```bash
git checkout main
git pull
git checkout -b release-X.Y.Z
```

Run the version update script with the full version including any RC suffix:
```bash
# For a release candidate:
./scripts/set-release-version.sh X.Y.Z-rcN

# For a final release:
./scripts/set-release-version.sh X.Y.Z
```

This updates version references across deployment scripts, docs, and manifests. Review changes with `git diff`.

If CRD or API type changes are included in this release, regenerate all manifests first:
```bash
make generate-all
```

Then regenerate the OLM bundle with the full version:
```bash
# For a release candidate:
make bundle VERSION=X.Y.Z-rcN

# For a final release:
make bundle VERSION=X.Y.Z
```

Commit and push:
```bash
git add -u config/ charts/ docs/ bundle/
git commit -s -m "Update version to X.Y.Z-rcN"
git push -u origin release-X.Y.Z
```

### 2. Create GitHub Release

Release branches use the full three-component format `release-X.Y.Z` (e.g. `release-0.6.0`), and all links, tags, and examples must use this format.

1. Go to [Releases](https://github.com/Kuadrant/mcp-gateway/releases)
2. Click **Draft a new release**
3. Click **Choose a tag** and create a new tag (e.g. `vX.Y.Z-rc1` or `vX.Y.Z`)
4. Set **Target** to your `release-X.Y.Z` branch
5. Set the release title (e.g. `vX.Y.Z-rc1` or `vX.Y.Z`)
6. Click **Generate release notes**
7. For release candidates: check **Set as a pre-release** (do not mark as latest)
8. For final releases: check **Set as the latest release**
9. Click **Publish release**

### 3. Verify Workflows Complete

1. [Build Images](https://github.com/Kuadrant/mcp-gateway/actions/workflows/images.yaml) - builds container images, OLM bundle and catalog with version tag
2. [Helm Chart Release](https://github.com/Kuadrant/mcp-gateway/actions/workflows/helm-release.yaml) - pushes chart to OCI registry

### 4. Verify Published Artifacts

```bash
# Replace X.Y.Z with the full version (e.g. 0.5.1-rc1 or 0.5.1)
VERSION=X.Y.Z
for image in \
  ghcr.io/kuadrant/mcp-gateway:v${VERSION} \
  ghcr.io/kuadrant/mcp-controller:v${VERSION} \
  ghcr.io/kuadrant/mcp-controller-bundle:v${VERSION} \
  ghcr.io/kuadrant/mcp-controller-catalog:v${VERSION}; do
  docker manifest inspect "$image" > /dev/null 2>&1 \
    && echo "✅ $image" || echo "❌ $image"
done
helm show chart oci://ghcr.io/kuadrant/charts/mcp-gateway --version ${VERSION} > /dev/null 2>&1 \
  && echo "✅ helm chart ${VERSION}" || echo "❌ helm chart ${VERSION}"
```

### 5. Create RC Test Matrix Issue (Release Candidates Only)

After verifying all artifacts are published, create a test matrix issue to track RC testing:

1. Go to [New Issue](https://github.com/Kuadrant/mcp-gateway/issues/new/choose)
2. Select the **RC Test Matrix** template
3. Replace `vX.Y.Z-rcN` with the actual RC version in the title and body
4. Replace `release-X.Y.Z` with the actual release branch name in all links
5. Submit the issue and share with the team for testing

Testing covers installation methods, E2E tests, integration tests, conformance tests, and documentation guide walkthroughs. See the [template](.github/ISSUE_TEMPLATE/rc-test-matrix.md) for the full checklist.

## Post-Release: Bump Version on Main

**Note**: Only do this after the final release is published, not for release candidates.

Update version references on `main` so they point to the new release:

```bash
git checkout main
git pull
git checkout -b bump-version-X.Y.Z
./scripts/set-release-version.sh X.Y.Z
make bundle VERSION=X.Y.Z
git add -u config/ charts/ docs/ bundle/
git commit -s -m "Update version to X.Y.Z"
git push -u origin bump-version-X.Y.Z
```

Open a PR targeting `main` with this change. This ensures documentation and scripts on `main` reference the latest release.

## Backporting Fixes to Release Branches

When a bug is discovered after a release branch has been cut:

1. **Always fix on main first** - Create a PR targeting `main` with the fix
2. **Cherry-pick to release branch** - After the fix is merged to main, cherry-pick the commit(s) to the release branch via a PR from a temp branch.
3. **Create a patch release** - If needed, create a new patch release (e.g., `vX.Y.Z-rcN+1`) from the release branch

This ensures:
- All fixes are captured in main for future releases
- Release branches stay in sync with tested fixes
- No fixes are lost between releases
