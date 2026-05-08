# Gevals / MCP Checker Evaluations

This directory contains configuration for running integration tests using [Gevals (MCP Checker)](https://github.com/bentito/gevals).

## Structure

*   `mcp-config.yaml`: Defines the MCP server connection (points to the local mcp-gateway instance).
*   `gemini-agent/`: Configuration for the LLM agent (using Gemini or OpenAI-compatible model).
*   `tasks/`: Task definitions for validation.

## Running Locally

### Option 1: Using the E2E infrastructure (Recommended)

This is the easiest way to run gevals tests against a local Kind cluster that matches the CI environment.

1.  **Start the E2E environment**:
    ```bash
    make ci-setup
    ```
2.  **Run the gevals tests**:
    ```bash
    export MODEL_KEY="your-api-key"
    export MODEL_BASE_URL="https://generativelanguage.googleapis.com/v1beta/openai/"
    make test-e2e-gevals
    ```
    This will set up a dedicated namespace `mcp-gevals`, deploy the necessary resources targeting the `e2e-1` gateway (port 8004), and run the tests.

3.  **Cleanup**:
    ```bash
    make test-e2e-gevals-cleanup
    ```

### Option 2: Using the Local Demo environment

1.  **Start the environment**:
    Make sure you have `kind`, `docker`, `kubectl` installed. Or use `make tools`.
    ```bash
    make local-env-setup
    ```
    This will deploy the mcp-gateway and test servers to a Kind cluster. The gateway will be accessible at `http://localhost:8001/mcp`.

2.  **Run MCP Checker**:
    You need to have `mcpchecker` installed.
    
    ```bash
    export MODEL_KEY="your-api-key"
    export MODEL_BASE_URL="https://generativelanguage.googleapis.com/v1beta/openai/"
    
    mcpchecker check --verbose evals/gemini-agent/eval.yaml
    ```

### LLM Dependencies

- **Paid LLM**: Set `MODEL_KEY` (OpenAI/Gemini key) and `MODEL_BASE_URL`.
- **Local Fallback (Ollama)**: If no keys are provided, CI will automatically start Ollama and pull `qwen2.5:1.5b`. 
- **Caching**: In CI, the Ollama model is cached to speed up runs. Locally, Ollama will reuse previously pulled models.
- **Resources**: Ensure your local machine or CI runner has at least 8GB RAM to run Ollama comfortably alongside the Kind cluster.

## Adding Tasks

Create a new YAML file in `evals/tasks/` following the schema:
```yaml
description: "Task description"
tools:
  - tool_name
steps:
  - instruction: "..."
    expectedTool: "tool_name"
    expectedArguments: { ... }
  - instruction: "..."
    expectedOutput: "..."
```
