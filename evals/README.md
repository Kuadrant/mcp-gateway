# Gevals / MCP Checker Evaluations

This directory contains configuration for running integration tests using [Gevals (MCP Checker)](https://github.com/bentito/gevals).

## Structure

*   `mcp-config.yaml`: Defines the MCP server connection (points to the local mcp-gateway instance).
*   `gemini-agent/`: Configuration for the LLM agent (using Gemini or OpenAI-compatible model).
*   `tasks/`: Task definitions for validation.

## Running Locally

1.  **Start the environment**:
    Make sure you have `kind`, `docker`, `kubectl` installed. Or use `make tools`.
    ```bash
    make local-env-setup
    ```
    This will deploy the mcp-gateway and test servers to a Kind cluster. The gateway will be accessible at `http://localhost:8001/mcp`.

2.  **Run MCP Checker**:
    You need to have `mcpchecker` (or `gevals`) installed, or use the docker image.
    
    Using docker:
    ```bash
    export MODEL_KEY="your-api-key"
    export MODEL_BASE_URL="https://generativelanguage.googleapis.com/v1beta/openai/" # Example for Gemini
    
    docker run --rm -it \
      --network host \
      -v $(pwd)/evals:/evals \
      -e MODEL_KEY=$MODEL_KEY \
      -e MODEL_BASE_URL=$MODEL_BASE_URL \
      quay.io/bentito/gevals:latest \
      check --config /evals/gemini-agent/eval.yaml
    ```
    
    (Note: Adjust image name if needed, assuming `quay.io/bentito/gevals` or similar exists as per issue description).

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
