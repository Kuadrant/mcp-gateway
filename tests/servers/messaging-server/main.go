// A mock MCP server for messaging and contacts demo
// Provides pre-canned responses for testing tool discovery and routing
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var httpAddr = flag.String(
	"http",
	"",
	"if set, use streamable HTTP at this address, instead of stdin/stdout",
)

type findContactsArgs struct {
	Query string `json:"query" jsonschema:"search query for contact name"`
}

type getContactArgs struct {
	ContactID string `json:"contact_id" jsonschema:"unique identifier for the contact"`
}

type sendMessageArgs struct {
	ContactID string `json:"contact_id" jsonschema:"recipient contact ID"`
	Message   string `json:"message" jsonschema:"message text to send"`
	Channel   string `json:"channel" jsonschema:"delivery channel: email, sms, or slack"`
}

type getMessagesArgs struct {
	ContactID string `json:"contact_id" jsonschema:"contact ID to retrieve messages for"`
}

type createGroupArgs struct {
	Name    string   `json:"name" jsonschema:"name of the messaging group"`
	Members []string `json:"members" jsonschema:"array of contact IDs to add as members"`
}

func findContacts(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params findContactsArgs,
) (*mcp.CallToolResult, any, error) {
	contacts := []map[string]interface{}{
		{
			"contact_id": "c001",
			"name":       "John Smith",
			"email":      "john.smith@example.com",
			"phone":      "+1-555-0101",
			"department": "Engineering",
		},
		{
			"contact_id": "c002",
			"name":       "Jane Doe",
			"email":      "jane.doe@example.com",
			"phone":      "+1-555-0102",
			"department": "Product",
		},
		{
			"contact_id": "c003",
			"name":       "Bob Johnson",
			"email":      "bob.johnson@example.com",
			"phone":      "+1-555-0103",
			"department": "Sales",
		},
	}

	response, _ := json.MarshalIndent(map[string]interface{}{
		"query":   params.Query,
		"results": contacts,
		"count":   len(contacts),
	}, "", "  ")

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(response)},
		},
	}, nil, nil
}

func getContact(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params getContactArgs,
) (*mcp.CallToolResult, any, error) {
	contact := map[string]interface{}{
		"contact_id":          params.ContactID,
		"name":                "John Smith",
		"email":               "john.smith@example.com",
		"phone":               "+1-555-0101",
		"department":          "Engineering",
		"title":               "Senior Software Engineer",
		"availability_status": "available",
	}

	response, _ := json.MarshalIndent(contact, "", "  ")

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(response)},
		},
	}, nil, nil
}

func sendMessage(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params sendMessageArgs,
) (*mcp.CallToolResult, any, error) {
	result := map[string]interface{}{
		"message_id": "msg_" + time.Now().Format("20060102150405"),
		"status":     "delivered",
		"timestamp":  time.Now().Format(time.RFC3339),
		"recipient":  "John Smith",
		"channel":    params.Channel,
		"preview":    params.Message[:min(len(params.Message), 50)],
	}

	response, _ := json.MarshalIndent(result, "", "  ")

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(response)},
		},
	}, nil, nil
}

func getMessages(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params getMessagesArgs,
) (*mcp.CallToolResult, any, error) {
	messages := []map[string]interface{}{
		{
			"message_id": "msg_001",
			"sender":     "John Smith",
			"text":       "Hey, can we schedule a meeting for tomorrow?",
			"timestamp":  "2026-04-01T10:30:00Z",
			"channel":    "slack",
		},
		{
			"message_id": "msg_002",
			"sender":     "You",
			"text":       "Sure! How about 2pm?",
			"timestamp":  "2026-04-01T10:35:00Z",
			"channel":    "slack",
		},
		{
			"message_id": "msg_003",
			"sender":     "John Smith",
			"text":       "Perfect, see you then.",
			"timestamp":  "2026-04-01T10:36:00Z",
			"channel":    "slack",
		},
	}

	response, _ := json.MarshalIndent(map[string]interface{}{
		"contact_id": params.ContactID,
		"messages":   messages,
		"count":      len(messages),
	}, "", "  ")

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(response)},
		},
	}, nil, nil
}

func createGroup(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params createGroupArgs,
) (*mcp.CallToolResult, any, error) {
	group := map[string]interface{}{
		"group_id":     "grp_" + time.Now().Format("20060102150405"),
		"name":         params.Name,
		"member_count": len(params.Members),
		"created_at":   time.Now().Format(time.RFC3339),
		"members":      params.Members,
	}

	response, _ := json.MarshalIndent(group, "", "  ")

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(response)},
		},
	}, nil, nil
}

func rpcPrintMiddleware(
	next mcp.MethodHandler,
) mcp.MethodHandler {
	return func(
		ctx context.Context,
		method string,
		req mcp.Request,
	) (mcp.Result, error) {
		fmt.Printf("MCP method started: method=%s, params=%v\n",
			method,
			req,
		)

		result, err := next(ctx, method, req)
		return result, err
	}
}

func main() {
	flag.Parse()

	server := mcp.NewServer(&mcp.Implementation{Name: "messaging-contacts-server"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_contacts",
		Description: "search for contacts by name",
	}, findContacts)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_contact",
		Description: "get detailed information for a specific contact",
	}, getContact)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_message",
		Description: "send a message to a contact via email, sms, or slack",
	}, sendMessage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_messages",
		Description: "retrieve recent messages with a contact",
	}, getMessages)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_group",
		Description: "create a new messaging group with specified members",
	}, createGroup)

	if *httpAddr != "" {
		server.AddReceivingMiddleware(rpcPrintMiddleware)
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)

		log.Printf("MCP handler will listen at %s", *httpAddr)
		httpServer := &http.Server{
			Addr:              *httpAddr,
			Handler:           handler,
			ReadHeaderTimeout: 3 * time.Second,
		}
		_ = httpServer.ListenAndServe()
	} else {
		log.Printf("MCP handler use stdio")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Printf("Server failed: %v", err)
		}
	}
}
