# Tool Discovery: Why Your MCP Gateway Needs a Tool Catalog

## Outline

### The problem: too many tools, not enough context

- MCP federation aggregates tools from many upstream servers behind a single endpoint
- An agent connecting to a gateway with 20+ servers gets 100-300+ tool definitions in one `tools/list` response
- Three concrete costs:
  - **Context bloat**: each tool definition includes name, description, and full JSON Schema input. Hundreds of these consume context window tokens that should be used for reasoning, conversation history, and user instructions
  - **Token cost**: every irrelevant tool definition is money wasted — agents pay per token on every request
  - **Degraded tool selection**: LLMs pick worse tools as the list grows. They hallucinate names, choose semantically similar but wrong tools, or fail to call any tool at all. Research shows accuracy drops from ~80% to ~14% as tool counts scale ([RAG-MCP, 2025](https://arxiv.org/abs/2505.03275))
- The irony: the gateway gets harder to use as it becomes more capable

### The gateway as registry and catalog

- A standalone MCP server knows its own tools. A gateway knows *everyone's* tools
- The gateway already acts as a **registry** — it tracks which servers exist, what tools they expose, how to reach them
- What's missing is the **catalog** layer — structured metadata that helps agents browse and select without ingesting full schemas
- Categories, hints, and tags turn the registry into a browsable catalog:
  - **Category**: domain classification ("dining", "scheduling", "payments") — coarse grouping
  - **Hint**: natural-language summary of what a server's tools do — gives the LLM enough to judge relevance without full schemas
  - **Tags** (future): finer-grained labels for cross-cutting concerns
- This metadata is cheap: a category string and a one-line hint cost far fewer tokens than the full tool schemas they represent

### Meta-tools: tools that help agents find tools

- The catalog is exposed through **meta-tools** — tools the gateway provides that aren't forwarded to any upstream server
- `tool_catalog` (or `discover_tools`): returns lightweight metadata — server names, categories, hints, tool names. No full schemas. Lets the LLM decide what's relevant based on intent, not keywords
- `select_tools`: scopes the session to a chosen subset. After calling this, `tools/list` returns only the selected tools with full schemas
- These are standard MCP tools — no protocol extensions, no client changes. Any MCP client can call them
- The gateway is uniquely positioned to provide these because it has the complete picture. No individual upstream server can offer a catalog of the entire federation

### The flow: progressive discovery

- Instead of dumping 200 tool schemas on turn 1, the agent sees 2 meta-tools
- Turn 1: agent calls `tool_catalog`, sees lightweight metadata, calls `select_tools` with the 5-10 tools relevant to the task
- Turn 2+: agent works with full schemas for only the tools it needs
- The LLM drives selection — it understands that "book a restaurant" implies calendar tools too, something keyword search would miss
- Cost: one extra turn. Benefit: dramatically fewer tokens, better tool selection, lower latency

### Why LLM-driven beats server-side search

- Server-side approaches (keyword, BM25, semantic search) match on words, not intent
- "Book a restaurant for Saturday" → keyword search finds restaurant tools, misses calendar tools
- The LLM understands the full user intent and can select across domains
- The catalog gives it just enough metadata to make good decisions without the token cost of full schemas

### Virtual servers: operator-curated catalogs

- Operators can pre-define scoped subsets using MCPVirtualServer resources
- Instead of listing individual tools, virtual servers reference categories — "give me everything in dining and scheduling"
- New tools added to a category automatically appear in matching virtual servers
- Virtual servers can expose their own route paths (`/dining/mcp`) for dedicated endpoints
- These appear in the catalog too — agents can discover that a curated scope already exists and skip manual selection

### What this means for platform teams

- Federation scales without degrading agent experience
- Operators add metadata once (category + hint per server registration), agents benefit automatically
- Auth filtering applies to the catalog — agents only discover tools they're authorized to use
- The threshold flag (`--discovery-tool-threshold`) auto-scales: small gateways show all tools, large ones enforce discovery

### Scale considerations

- The catalog-based approach will itself naturally become a limiter with very large tool sets and may degrade LLM selection quality
- A near-term mitigation is adding an optional **category filter** to `tool_catalog` (e.g. `category: "dining"`). This lets agents narrow the catalog to a specific domain without scanning the full list, keeping responses small even at large scale
- In future iterations, we may explore **server-side retrieval** using BM25 and embedding-based search at the gateway layer. Prior work in this space includes [tools_rag](https://github.com/blublinsky/tools_rag) (Python) and [tdt](https://github.com/maleck13/tdt) (Go, supports BM25 + optional embeddings + RRF). This would let agents query by natural-language intent and receive ranked results rather than browsing a flat catalog. The tradeoff is increased deployment complexity (embedding model, index storage) but significantly better filtering for very large tool sets
