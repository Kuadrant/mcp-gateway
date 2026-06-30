# Manual Test Cases

Manual test cases in `tests/manual-testcases/<upcoming-release>.md` are expensive and should be used sparingly. Create the file if it does not exist.

Manual test cases are needed:

- Features requiring external infrastructure impractical for e2e (real cloud providers, third-party SaaS)
- Complex observability verification (distributed tracing across multiple systems, Grafana dashboards)
- Bug fixes that cannot be adequately covered by automated tests due to infrastructure constraints
- Features with critical browser/UI interaction that cannot be automated
- Brand new guide added to `docs/guides/` that introduces a complete workflow not previously documented

Manual test cases are not needed:

- Demo code in `demos/`
- Example configurations in `config/samples/`
- Documentation updates, improvements or new sections added to existing guides in `docs/guides`
- Test improvements
- Features/Bug fixes with adequate e2e or integration test coverage
