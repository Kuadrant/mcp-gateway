# CRD Changes

When adding or changing CRD fields in `api/v1alpha1/`:

- Run `make generate-all` to regenerate deepcopy, CRDs, and sync Helm
- Update the relevant controller reconciler
- Update status conditions if needed
- Add controller unit tests
- Add e2e test coverage
- Update the corresponding API reference doc in `docs/reference/`
