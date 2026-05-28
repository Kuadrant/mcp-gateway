window.BENCHMARK_DATA = {
  "lastUpdate": 1779982243434,
  "repoUrl": "https://github.com/Kuadrant/mcp-gateway",
  "entries": {
    "MCP Gateway Performance": [
      {
        "commit": {
          "author": {
            "name": "Jason Madigan",
            "username": "jasonmadigan",
            "email": "jason@jasonmadigan.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "238a39da554279a3bced12edb96432c6b1a39518",
          "message": "fix: add explicit runAsUser to k6 benchmark job (#1055)\n\n* fix: add explicit runAsUser to k6 benchmark job\n\nSigned-off-by: Jason Madigan <jason@jasonmadigan.com>\n\n* fix: pin k6 user UID/GID in Dockerfile to match job manifest\n\nSigned-off-by: Jason Madigan <jason@jasonmadigan.com>\n\n---------\n\nSigned-off-by: Jason Madigan <jason@jasonmadigan.com>",
          "timestamp": "2026-05-28T15:14:54Z",
          "url": "https://github.com/Kuadrant/mcp-gateway/commit/238a39da554279a3bced12edb96432c6b1a39518"
        },
        "date": 1779982242595,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "p95_tool_call_ms",
            "value": 0,
            "unit": "ms"
          },
          {
            "name": "p99_tool_call_ms",
            "value": 0,
            "unit": "ms"
          },
          {
            "name": "avg_tool_call_ms",
            "value": 0,
            "unit": "ms"
          },
          {
            "name": "tool_error_rate",
            "value": 0,
            "unit": "percent"
          },
          {
            "name": "session_fail_rate",
            "value": 0,
            "unit": "percent"
          }
        ]
      }
    ]
  }
}