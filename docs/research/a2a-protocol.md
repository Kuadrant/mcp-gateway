# A2A (Agent-to-Agent) Protocol Research

## Overview

The Agent2Agent (A2A) protocol is an open protocol from Google (announced April 2025 at Cloud Next) that enables communication and interoperability between opaque AI agent systems. It is now an open-source project under the Linux Foundation at [github.com/a2aproject/A2A](https://github.com/a2aproject/A2A). As of v0.3 (July 2025), it has over 150 supporting organizations.

**Core problem**: AI agents built with different frameworks, languages, or by different vendors need a common way to discover each other, negotiate capabilities, and collaborate on tasks -- without exposing their internal state, memory, or tool implementations.

**Key principle -- Agent Opacity**: Agents collaborate without sharing proprietary logic, internal memory, or specific tool implementations. This is a fundamental design difference from MCP, where tools are transparent.

### Source URLs

- Specification: https://a2a-protocol.org/latest/specification/
- GitHub: https://github.com/a2aproject/A2A
- Proto definition: https://github.com/a2aproject/A2A/blob/main/specification/a2a.proto
- Google blog announcement: https://developers.googleblog.com/en/a2a-a-new-era-of-agent-interoperability/
- Developer guide (A2A vs MCP): https://developers.googleblog.com/developers-guide-to-ai-agent-protocols/

---

## Discovery: Agent Card

Agents publish an **AgentCard** -- a JSON metadata document describing identity, capabilities, skills, and security requirements. By convention, it is served at `/.well-known/agent.json`.

### AgentCard fields (all required unless noted)

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Human-readable agent name |
| `description` | string | What the agent does |
| `version` | string | Agent version (e.g. "1.0.0") |
| `supported_interfaces` | AgentInterface[] | Ordered list of endpoints (first is preferred) |
| `capabilities` | AgentCapabilities | Streaming, push notifications, extensions |
| `default_input_modes` | string[] | Accepted input media types |
| `default_output_modes` | string[] | Supported output media types |
| `skills` | AgentSkill[] | List of capabilities |
| `security_schemes` | map<string, SecurityScheme> | Auth methods |
| `security_requirements` | SecurityRequirement[] | Which schemes are required |
| `provider` | AgentProvider | (optional) Organization info |
| `documentation_url` | string | (optional) |
| `signatures` | AgentCardSignature[] | (optional) JWS signatures for verification |
| `icon_url` | string | (optional) |

### AgentInterface

Each interface declares a URL, protocol binding, and protocol version:

```
{
  "url": "https://api.example.com/a2a/v1",
  "protocol_binding": "JSONRPC",  // or "GRPC" or "HTTP+JSON"
  "protocol_version": "0.3"
}
```

### AgentSkill

Skills describe what an agent can do. They are descriptive (not executable like MCP tools):

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique identifier |
| `name` | string | Human-readable name |
| `description` | string | What the skill does |
| `tags` | string[] | Keywords for discovery |
| `examples` | string[] | (optional) Example prompts |
| `input_modes` | string[] | (optional) Override agent defaults |
| `output_modes` | string[] | (optional) Override agent defaults |

### Extended Agent Card

Agents can optionally serve a richer card at an authenticated endpoint (`/extendedAgentCard`), revealing additional skills or details only to authenticated clients.

---

## Transport and Protocol Bindings

A2A supports three protocol bindings:

1. **JSON-RPC 2.0 over HTTP(S)** -- the original and most common binding
2. **gRPC** -- added in v0.3 (July 2025), full service definition with streaming
3. **HTTP+JSON** -- RESTful HTTP with JSON bodies

The protobuf service definition (`a2a.proto`) includes HTTP annotations that map to REST-style endpoints:

| Operation | HTTP Method | Path |
|-----------|------------|------|
| SendMessage | POST | `/message:send` |
| SendStreamingMessage | POST | `/message:stream` |
| GetTask | GET | `/tasks/{id}` |
| ListTasks | GET | `/tasks` |
| CancelTask | POST | `/tasks/{id}:cancel` |
| SubscribeToTask | GET | `/tasks/{id}:subscribe` |
| Push notification CRUD | various | `/tasks/{task_id}/pushNotificationConfigs/...` |
| GetExtendedAgentCard | GET | `/extendedAgentCard` |

Multi-tenancy is supported via an optional `{tenant}` path prefix.

---

## Core Data Model

### Task

The fundamental unit of work. Server-generated ID. Has a lifecycle with defined states.

| Field | Type | Required |
|-------|------|----------|
| `id` | string | yes |
| `context_id` | string | no (groups related tasks) |
| `status` | TaskStatus | yes |
| `artifacts` | Artifact[] | no |
| `history` | Message[] | no |
| `metadata` | Struct | no |

### TaskState (lifecycle)

```
                    +---> COMPLETED (terminal)
                    |
SUBMITTED ---> WORKING ---> FAILED (terminal)
                    |
                    +---> CANCELED (terminal)
                    |
                    +---> REJECTED (terminal)
                    |
                    +---> INPUT_REQUIRED (interrupted, awaits user input)
                    |
                    +---> AUTH_REQUIRED (interrupted, awaits auth)
```

- **SUBMITTED**: Task acknowledged
- **WORKING**: Actively processing
- **INPUT_REQUIRED**: Agent needs more information from client
- **AUTH_REQUIRED**: Agent needs authentication credentials
- **COMPLETED**: Successfully finished (terminal)
- **FAILED**: Error (terminal)
- **CANCELED**: Client canceled (terminal)
- **REJECTED**: Agent refused the task (terminal)

### Message

A communication turn between client and agent:

| Field | Type | Required |
|-------|------|----------|
| `message_id` | string | yes |
| `role` | Role (USER or AGENT) | yes |
| `parts` | Part[] | yes |
| `context_id` | string | no |
| `task_id` | string | no |
| `reference_task_ids` | string[] | no |
| `metadata` | Struct | no |
| `extensions` | string[] | no |

### Part

The smallest content unit. A discriminated union:

- **text**: plain string content
- **raw**: binary bytes (base64 in JSON)
- **url**: URL pointing to content
- **data**: arbitrary structured JSON (`google.protobuf.Value`)

Each part also has optional `metadata`, `filename`, and `media_type` fields.

### Artifact

Output generated by an agent during task execution:

| Field | Type | Required |
|-------|------|----------|
| `artifact_id` | string | yes |
| `name` | string | no |
| `description` | string | no |
| `parts` | Part[] | yes |
| `metadata` | Struct | no |

---

## RPC Methods

### Core operations

| Method | Input | Output | Description |
|--------|-------|--------|-------------|
| `SendMessage` | SendMessageRequest | SendMessageResponse (Task or Message) | Send a message, may create/update a task |
| `SendStreamingMessage` | SendMessageRequest | stream StreamResponse | Same but with real-time updates |
| `GetTask` | GetTaskRequest | Task | Get current task state |
| `ListTasks` | ListTasksRequest | ListTasksResponse | Query tasks with filters and pagination |
| `CancelTask` | CancelTaskRequest | Task | Cancel a running task |
| `SubscribeToTask` | SubscribeToTaskRequest | stream StreamResponse | Subscribe to updates on existing task |

### Push notification operations

| Method | Description |
|--------|-------------|
| `CreateTaskPushNotificationConfig` | Register a webhook for task updates |
| `GetTaskPushNotificationConfig` | Get webhook config |
| `ListTaskPushNotificationConfigs` | List all webhooks for a task |
| `DeleteTaskPushNotificationConfig` | Remove a webhook |

### Discovery

| Method | Description |
|--------|-------------|
| `GetExtendedAgentCard` | Fetch authenticated agent card with additional details |

---

## Streaming

Two streaming mechanisms:

1. **SendStreamingMessage**: Client sends a message, server returns a stream of `StreamResponse` events
2. **SubscribeToTask**: Client subscribes to an existing task's updates

`StreamResponse` is a union type containing one of:
- `task`: Full Task object
- `message`: A Message from the agent
- `status_update`: TaskStatusUpdateEvent (state change)
- `artifact_update`: TaskArtifactUpdateEvent (new/updated output)

Artifact updates support chunking via `append` and `last_chunk` fields.

Events must be delivered in order. Stream terminates when the task reaches a terminal state.

---

## Push Notifications (Async)

For long-running tasks, clients can register webhooks:

```
TaskPushNotificationConfig {
  id: string
  task_id: string
  url: string          // webhook endpoint
  token: string        // session token
  authentication: AuthenticationInfo  // credentials for the agent to use when calling the webhook
}
```

The agent POSTs `StreamResponse` payloads to the webhook URL. This supports fire-and-forget patterns where the client does not need to maintain a long-lived connection.

---

## Authentication

Security schemes align with OpenAPI 3.2 specification:

| Scheme | Description |
|--------|-------------|
| **API Key** | Key in header, query, or cookie |
| **HTTP Auth** | Basic, Bearer (with optional format hint like JWT) |
| **OAuth 2.0** | Authorization Code (with PKCE), Client Credentials, Device Code |
| **OpenID Connect** | Via OIDC Discovery URL |
| **Mutual TLS** | Client certificate authentication |

Schemes are declared in the AgentCard's `security_schemes` map and applied via `security_requirements`. Individual skills can override the agent-level security requirements.

---

## SendMessage Configuration

Clients can configure behavior per request:

| Field | Description |
|-------|-------------|
| `accepted_output_modes` | Media types the client can accept |
| `task_push_notification_config` | Webhook config for async updates |
| `history_length` | Max messages to include in response |
| `return_immediately` | If true, return after task creation without waiting for completion |

---

## A2A vs MCP: Key Differences

| Aspect | MCP (Model Context Protocol) | A2A (Agent-to-Agent) |
|--------|------------------------------|----------------------|
| **Purpose** | Connect agents to tools and data sources | Connect agents to other agents |
| **Transparency** | Tools are transparent -- schema, parameters, return types exposed | Agents are opaque -- internal logic hidden |
| **Interaction model** | Tool invocation (function call with typed params) | Task-oriented (natural language messages with multimodal parts) |
| **Discovery** | Server advertises tools with JSON Schema | Agent publishes AgentCard with skills (descriptive, no schema) |
| **State** | Stateless tool calls | Stateful tasks with lifecycle |
| **Communication** | JSON-RPC 2.0, stdio/SSE/streamable HTTP | JSON-RPC 2.0 / gRPC / HTTP+JSON over HTTPS |
| **Multi-turn** | Not a core concept | First-class: INPUT_REQUIRED state, message history |
| **Content** | Structured (typed tool results) | Multimodal (text, files, structured data) |
| **Auth** | Not standardized in spec | OpenAPI-aligned security schemes in AgentCard |
| **Long-running work** | Not supported | Built-in: task states, streaming, push notifications |

### Complementary relationship

MCP and A2A are designed to work together:
- **MCP** handles the "vertical" connection: agent to databases, APIs, file systems, tools
- **A2A** handles the "horizontal" connection: agent to agent collaboration

Example flow: An agent uses MCP to query a local database for inventory data, then uses A2A to consult a remote pricing agent for quotes.

---

## Relevance to MCP Gateway

Several aspects of A2A are relevant to the mcp-gateway project:

1. **Discovery**: A2A's AgentCard at `/.well-known/agent.json` is analogous to MCP tool discovery but for agents. The gateway could potentially serve an AgentCard describing its federated capabilities.

2. **Gateway as A2A endpoint**: The broker already federates MCP tools. It could also present itself as an A2A agent, accepting natural-language task requests and routing them to appropriate upstream MCP servers.

3. **A2A-to-MCP bridging**: The gateway sits at the intersection -- it could translate A2A task requests into MCP tool calls, and MCP tool results into A2A artifacts/messages.

4. **Authentication alignment**: A2A's security schemes (OAuth 2.0, API keys, OIDC) align with what the gateway already supports via AuthPolicy and credential management.

5. **Streaming**: A2A's streaming model (SSE/gRPC server streaming) is similar to MCP's SSE transport, which the broker already handles.

6. **Multi-tenancy**: A2A has built-in tenant path prefixes, which could map to the gateway's namespace-based multi-tenancy.
