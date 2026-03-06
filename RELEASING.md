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

Run the version update script:
```bash
./scripts/set-release-version.sh X.Y.Z
```

This updates version references in:
- `config/openshift/deploy_openshift.sh`
- `charts/sample_local_helm_setup.sh`
- `docs/guides/quick-start.md`
- `config/manifests/bases/mcp-gateway.clusterserviceversion.yaml`
- `config/deploy/olm/catalogsource.yaml`

Then regenerate the OLM bundle:
```bash
make bundle VERSION=X.Y.Z
```

Commit and push:
```bash
git add config/openshift/deploy_openshift.sh charts/sample_local_helm_setup.sh \
  docs/guides/quick-start.md config/manifests/bases/ config/deploy/olm/catalogsource.yaml bundle/
git commit -s -m "Update version to X.Y.Z"
git push -u origin release-X.Y.Z
```

### 2. Create GitHub Release

1. Go to [Releases](https://github.com/Kuadrant/mcp-gateway/releases)
2. Click **Draft a new release**
3. Click **Choose a tag** and create a new tag `vX.Y.Z` (or vX.Y.Z-rcN, e.g. v0.5.0-rc1, for a release candidate)
4. Set **Target** to your `release-X.Y.Z` branch
5. Set the release title to `vX.Y.Z` (or vX.Y.Z-rcN)
6. Click **Generate release notes**
7. Click **Publish release**

### 3. Verify Workflows Complete

1. [Build Images](https://github.com/Kuadrant/mcp-gateway/actions/workflows/images.yaml) - builds container images, OLM bundle and catalog with version tag
2. [Helm Chart Release](https://github.com/Kuadrant/mcp-gateway/actions/workflows/helm-release.yaml) - pushes chart to OCI registry

### 4. Verify Published Artifacts

```bash
docker pull ghcr.io/kuadrant/mcp-gateway:vX.Y.Z
docker pull ghcr.io/kuadrant/mcp-controller:vX.Y.Z
helm show chart oci://ghcr.io/kuadrant/charts/mcp-gateway --version X.Y.Z
docker pull ghcr.io/kuadrant/mcp-controller-bundle:vX.Y.Z
docker pull ghcr.io/kuadrant/mcp-controller-catalog:vX.Y.Z
```

## Post-Release: Bump Version on Main

After the release is published, update version references on `main` so they point to the new release:

```bash
git checkout main
git pull
git checkout -b bump-version-X.Y.Z
./scripts/set-release-version.sh X.Y.Z
make bundle VERSION=X.Y.Z
git add config/openshift/deploy_openshift.sh charts/sample_local_helm_setup.sh \
  docs/guides/quick-start.md config/manifests/bases/ config/deploy/olm/catalogsource.yaml bundle/
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
