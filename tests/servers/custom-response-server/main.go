package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

type JSONRPC struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      int                    `json:"id"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params"`
}

func main() {
	var httpAddr = flag.String(
		"http",
		"0.0.0.0:9090",
		"if set, use streamable HTTP at this address, instead of stdin/stdout",
	)

	flag.Parse()

	toolsList := `{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "tools": [
      {
        "name": "custom response code",
        "title": "Return a custom response",
        "description": "Return a custom response",
        "inputSchema": {
          "type": "object",
          "properties": {
            "responseCode": {
              "type": "int",
              "description": "response code"
            }
          },
          "required": ["responseCode"]
        }
      }
    ]
  }
}`

	initResp := `{
		"jsonrpc": "2.0",
		"id": 1,
		"result": {
			"protocolVersion": "2025-06-18",
			"capabilities": {
			"logging": {},
			"resources": {
				"subscribe": true,
				"listChanged": true
			},
			"tools": {
				"listChanged": true
			}
			},
			"serverInfo": {
			"name": "ExampleServer",
			"title": "Example Server Display Name",
			"version": "1.0.0"
			},
			"instructions": ""
		}
		}`
	var pingRes = func(id int) []byte {
		return []byte(fmt.Sprintf(`{
  "jsonrpc": "2.0",
  "id": %v,
  "result": {}
}`, id))
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("custom response %s request to %s", r.Method, r.URL.Path)

		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(405)
			return
		}
		if r.Method == "POST" {
			jsonrpcReq := &JSONRPC{}
			dec := json.NewDecoder(r.Body)
			defer r.Body.Close()
			if err := dec.Decode(jsonrpcReq); err != nil {
				log.Println("failed to decode json rpc ", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				w.Write([]byte(`{"status": "bad request not json rpc"}`))
				return
			}
			log.Println("handling json rpc ", jsonrpcReq)
			if jsonrpcReq.Method == "notifications/initialized" {
				log.Println("initialized")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				return
			}
			if jsonrpcReq.Method == "initialize" {
				log.Println("initialize")
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("mcp-session-id", "my amazing session")
				w.WriteHeader(200)
				w.Write([]byte(initResp))
				return
			}
			if jsonrpcReq.Method == "tools/list" {
				log.Println("tools/list")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				w.Write([]byte(toolsList))
				return
			}
			if jsonrpcReq.Method == "ping" {
				log.Println("ping")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				w.Write(pingRes(jsonrpcReq.ID))
				return
			}
			if jsonrpcReq.Method == "tools/call" {
				log.Println("tools/call")
				args := jsonrpcReq.Params["arguments"].(map[string]interface{})
				log.Println("arguments", args)
				response := args["responseCode"].(float64)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(int(response))
				//w.Write([]byte(`{"status":"sent response"}`))
				return
			}

		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		w.Write([]byte(`{"status": "session invalid"}`))
	})

	server := &http.Server{
		Addr:              *httpAddr,
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
	}

	log.Printf("Starting custom response HTTP server on %s", *httpAddr)
	if err := server.ListenAndServe(); err != nil {
		log.Printf("HTTP custom server error: %v", err)
	}

}
