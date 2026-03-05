# Documentation Guidelines

## Structure

Two documentation areas, each serving a different audience:

- `docs/guides/` — user-facing documentation for operators and developers using the MCP Gateway
- `docs/design/` — developer-facing documentation for contributors working on the MCP Gateway codebase

## Publishing

Docs are published at docs.kuadrant.io via [Kuadrant/docs.kuadrant.io](https://github.com/Kuadrant/docs.kuadrant.io). That repo uses mkdocs and has a nav structure in `mkdocs.yml` with an "MCP Gateway" section that references files from this repo. The nav groups guides into categories (About, Configuration, MCP Servers, Security, Support, etc.).

`docs/guides/README.md` is the local index of all guides. When adding a new guide, add it to both:
1. `docs/guides/README.md` in this repo
2. The MCP Gateway nav section in the docs.kuadrant.io `mkdocs.yml` (separate PR)

## Writing Guides (docs/guides/)

Follow the [diataxis](https://diataxis.fr/) framework. Most guides are **how-to guides**, not tutorials:

- **How-to guide**: goal-oriented, assumes the reader already has a working system and wants to accomplish a specific task. No hand-holding through a known environment.
- **Tutorial**: learning-oriented, walks through a specific scenario in a controlled environment. We rarely write these.

### Conventions

- **No repo-internal references**: don't point users at files in `config/` or Makefile targets. Users install via Helm or kustomize and don't have the repo checked out. Provide inline YAML or `kubectl` commands instead.
- **No assumed environment**: don't assume Kind, minikube, or any specific cluster setup. Only assume the MCP Gateway is installed.
- **Use `kubectl set env` for deployment config**: consistent pattern across guides (see authentication.md, scaling.md).
- **Use `kubectl apply -f - <<EOF`** for inline resource creation.
- **Numbered steps**: Step 1, Step 2, etc. Each step has a clear action and a verification command.
- **Prerequisites section**: list what must already be in place before starting.
- **Next Steps section**: link to related guides at the end.
- **Persona**: the reader is a platform operator or developer who has the gateway running and wants to configure a specific capability.

### Existing guide patterns to follow

- `authentication.md` — configuring OAuth with env vars and AuthPolicy
- `authorization.md` — configuring tool-level access control
- `scaling.md` — configuring Redis and horizontal scaling
- `configure-mcp-gateway-listener-and-router.md` — adding listeners and routes

## Writing Design Docs (docs/design/)

Design docs explain architecture, component responsibilities, and design decisions for contributors. They describe how things work internally, not how to use them. Target audience is someone reading the source code.

## Style

- Match the code style in the root `CLAUDE.md`: minimal comments, no emojis, no AI-style formatting.
- Keep guides concise. Explain what the user needs to do, not the full internal design.
- Use code blocks for all commands and YAML.
- Use `> **Note:**` for important callouts.
