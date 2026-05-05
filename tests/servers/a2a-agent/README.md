# a2a-fixture-agent

A minimal local A2A agent for design validation. No external dependencies, no gateway changes.

Serves two skills:
- **echo**: returns the input text unchanged
- **reverse**: returns the input text reversed

## Run locally

```bash
go run . --http localhost:8090
```

## Build and run with Docker

```bash
docker build --load --tag a2a-fixture-agent .
docker run --publish 8090:8090 a2a-fixture-agent /a2a-fixture-agent --http 0.0.0.0:8090
```

## Endpoints

| path | description |
|---|---|
| `GET /.well-known/agent.json` | agent card |
| `POST /` | JSON-RPC 2.0 task endpoint |

## Supported JSON-RPC methods

| method | description |
|---|---|
| `tasks/send` | create task, returns completed task with artifact |
| `tasks/sendSubscribe` | create task, streams status events as SSE |
| `tasks/get` | get task by ID |
| `tasks/cancel` | cancel task by ID |
| `tasks/pushNotificationConfig/set` | stub, always returns ok |

## curl examples

**Get agent card:**
```bash
curl http://localhost:8090/.well-known/agent.json | jq
```

**Send a task (echo):**
```bash
curl -X POST http://localhost:8090 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tasks/send",
    "params": {
      "id": "task-001",
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "hello world"}]
      }
    }
  }' | jq
```

Expected response:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "id": "task-001",
    "status": {"state": "completed", "timestamp": "..."},
    "artifacts": [{"name": "result", "index": 0, "parts": [{"type": "text", "text": "hello world"}]}]
  }
}
```

**Send a task (reverse):**
```bash
curl -X POST http://localhost:8090 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tasks/send",
    "params": {
      "id": "task-002",
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "reverse hello"}]
      }
    }
  }' | jq
```

Expected artifact text: `"olleh"`

**Stream a task:**
```bash
curl -X POST http://localhost:8090 \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tasks/sendSubscribe",
    "params": {
      "id": "task-003",
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "hello stream"}]
      }
    }
  }'
```

Expected SSE events: `submitted` -> `working` -> `completed` (with artifact on final event).

**Get a task:**
```bash
curl -X POST http://localhost:8090 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tasks/get","params":{"id":"task-001"}}' | jq
```

**Cancel a task:**
```bash
curl -X POST http://localhost:8090 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":5,"method":"tasks/cancel","params":{"id":"task-001"}}' | jq
```
