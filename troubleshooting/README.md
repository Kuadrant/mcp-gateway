# MCP Gateway Troubleshooting Agent

An AI system prompt that diagnoses MCP Gateway deployment issues by analysing must-gather output and matching against known failure scenarios. Works with any AI assistant.

## Who this is for

Customers, Red Hat support engineers, and anyone debugging an MCP Gateway deployment on OpenShift or Kubernetes.

## How to use it

1. Open `agent.md` from this folder
2. Copy the full contents
3. Paste it as the system prompt or first message in your AI assistant of choice:
   - **Claude / Claude Code** — paste at the start of a new conversation, or use as a project instruction
   - **Cursor** — add as a `.cursorrules` file or paste into the system prompt field
   - **ChatGPT** — paste as the first message or into a Custom GPT system prompt
   - **GitHub Copilot Chat** — paste at the start of a chat session
   - **Any other tool** — the file is plain markdown, it works anywhere

4. The agent will ask you to either describe your symptoms or paste must-gather output, then walk you through diagnosis.

## What the agent knows

Five known failure scenarios derived from real customer issues:

| Symptom | Likely cause |
|---|---|
| Tool calls fail with "no such host" DNS error | Wrong namespace in `privateHost` |
| `tools/list` returns empty despite Registration being Ready | Secret sync delay, duplicate prefixes, or VirtualServer misconfiguration |
| Broker fails with "http: server gave HTTP response to HTTPS client" | HTTPRoute backendRef pointing at a TLS port |
| Tool calls return 401 despite `credentialRef` configured | `credentialRef` only covers discovery, not tool call path |
| MCPServerRegistration stuck NotReady with no status | Broken dependency chain — parentRef, ReferenceGrant, or MCPGatewayExtension |

## Found a bug?

If the agent concludes your issue is not a configuration problem but a defect in MCP Gateway itself:

### External customers

Open a GitHub issue at **https://github.com/kuadrant/mcp-gateway/issues** using the bug report template. Include:

- MCP Gateway and OpenShift/Kubernetes versions
- Steps to reproduce
- Must-gather output (redact any credentials, tokens, or sensitive values before sharing)
- Which known scenario your issue does NOT match

### Red Hat subscription customers

Open a support case at **https://access.redhat.com/support**.

### Red Hat internal

Reach the RHCL team in **`#forum-connectivity-link`** on Slack.

## Privacy note

Pod logs may contain request headers, JWT tokens, session IDs, or credential fragments. Review logs before sharing them outside your organisation.

## Improving the agent

To add a new failure scenario, edit `agent.md` and add it to the **Known Failure Scenarios** section following the existing format, then open a PR. The RHCL team reviews agent changes alongside code changes.
