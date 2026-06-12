# E2E Tests

- Use Ginkgo framework, test cases defined in `tests/e2e/test_cases.md`
- Use direct port-forwards to `deployment/mcp-gateway`
- Clean up resources before creating them
- Test servers live in `tests/servers/` — create new ones for specific scenarios
- Test server images are built and pushed in `.github/workflows/test-images.yaml`
- When e2e coverage is insufficient, consider manual test cases (see `manual-test-cases.md` for criteria)
