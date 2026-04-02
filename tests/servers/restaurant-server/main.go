// A mock MCP server for restaurant booking with pre-canned responses
package main

import (
	"context"
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

type searchRestaurantsArgs struct {
	Cuisine  string `json:"cuisine" jsonschema:"cuisine type (e.g. italian, chinese, japanese)"`
	Location string `json:"location" jsonschema:"location or area (e.g. Dublin, Temple Bar)"`
}

func searchRestaurants(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params searchRestaurantsArgs,
) (*mcp.CallToolResult, any, error) {
	// mock restaurant data
	mockData := fmt.Sprintf(`[
  {
    "id": "rest-001",
    "name": "La Bella %s",
    "cuisine": "%s",
    "rating": 4.5,
    "address": "123 Main St, %s"
  },
  {
    "id": "rest-002",
    "name": "The Golden %s Kitchen",
    "cuisine": "%s",
    "rating": 4.2,
    "address": "456 Oak Ave, %s"
  },
  {
    "id": "rest-003",
    "name": "%s Delight",
    "cuisine": "%s",
    "rating": 4.8,
    "address": "789 Park Lane, %s"
  }
]`, params.Cuisine, params.Cuisine, params.Location,
		params.Cuisine, params.Cuisine, params.Location,
		params.Cuisine, params.Cuisine, params.Location)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: mockData},
		},
	}, nil, nil
}

type getRestaurantDetailsArgs struct {
	RestaurantID string `json:"restaurant_id" jsonschema:"unique restaurant identifier"`
}

func getRestaurantDetails(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params getRestaurantDetailsArgs,
) (*mcp.CallToolResult, any, error) {
	mockDetails := fmt.Sprintf(`{
  "id": "%s",
  "name": "La Bella Italia",
  "cuisine": "italian",
  "rating": 4.5,
  "address": "123 Main St, Dublin",
  "opening_hours": "Mon-Sun 12:00-22:00",
  "menu_highlights": ["Margherita Pizza", "Carbonara", "Tiramisu"],
  "phone": "+353-1-555-0123"
}`, params.RestaurantID)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: mockDetails},
		},
	}, nil, nil
}

type checkAvailabilityArgs struct {
	RestaurantID string `json:"restaurant_id" jsonschema:"unique restaurant identifier"`
	Date         string `json:"date" jsonschema:"booking date (YYYY-MM-DD)"`
	PartySize    int    `json:"party_size" jsonschema:"number of guests"`
	Time         string `json:"time" jsonschema:"preferred time (HH:MM)"`
}

func checkAvailability(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params checkAvailabilityArgs,
) (*mcp.CallToolResult, any, error) {
	mockAvailability := fmt.Sprintf(`{
  "restaurant_id": "%s",
  "date": "%s",
  "party_size": %d,
  "available_slots": [
    {"time": "18:00", "available": true},
    {"time": "19:30", "available": true},
    {"time": "21:00", "available": true}
  ]
}`, params.RestaurantID, params.Date, params.PartySize)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: mockAvailability},
		},
	}, nil, nil
}

type makeReservationArgs struct {
	RestaurantID string `json:"restaurant_id" jsonschema:"unique restaurant identifier"`
	Date         string `json:"date" jsonschema:"booking date (YYYY-MM-DD)"`
	Time         string `json:"time" jsonschema:"booking time (HH:MM)"`
	PartySize    int    `json:"party_size" jsonschema:"number of guests"`
	Name         string `json:"name" jsonschema:"name for the reservation"`
}

func makeReservation(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params makeReservationArgs,
) (*mcp.CallToolResult, any, error) {
	reservationID := fmt.Sprintf("RES-%d", time.Now().Unix())
	mockConfirmation := fmt.Sprintf(`{
  "reservation_id": "%s",
  "restaurant_name": "La Bella Italia",
  "date": "%s",
  "time": "%s",
  "party_size": %d,
  "name": "%s",
  "confirmation": "Reservation confirmed! Please arrive 10 minutes before your booking time."
}`, reservationID, params.Date, params.Time, params.PartySize, params.Name)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: mockConfirmation},
		},
	}, nil, nil
}

type cancelReservationArgs struct {
	ReservationID string `json:"reservation_id" jsonschema:"unique reservation identifier"`
}

func cancelReservation(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params cancelReservationArgs,
) (*mcp.CallToolResult, any, error) {
	mockCancellation := fmt.Sprintf(`{
  "reservation_id": "%s",
  "status": "cancelled",
  "message": "Your reservation has been successfully cancelled. We hope to see you again soon!"
}`, params.ReservationID)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: mockCancellation},
		},
	}, nil, nil
}

func main() {
	flag.Parse()

	server := mcp.NewServer(&mcp.Implementation{Name: "restaurant booking server"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "search_restaurants", Description: "search for restaurants by cuisine and location"}, searchRestaurants)
	mcp.AddTool(server, &mcp.Tool{Name: "get_restaurant_details", Description: "get detailed information about a specific restaurant"}, getRestaurantDetails)
	mcp.AddTool(server, &mcp.Tool{Name: "check_availability", Description: "check table availability for a restaurant"}, checkAvailability)
	mcp.AddTool(server, &mcp.Tool{Name: "make_reservation", Description: "book a table at a restaurant"}, makeReservation)
	mcp.AddTool(server, &mcp.Tool{Name: "cancel_reservation", Description: "cancel an existing reservation"}, cancelReservation)

	if *httpAddr != "" {
		server.AddReceivingMiddleware(rpcPrintMiddleware)
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)

		log.Printf("MCP handler will listen at %s", *httpAddr)
		server := &http.Server{
			Addr:              *httpAddr,
			Handler:           handler,
			ReadHeaderTimeout: 3 * time.Second,
		}
		_ = server.ListenAndServe()
	} else {
		log.Printf("MCP handler use stdio")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Printf("Server failed: %v", err)
		}
	}
}

// Simple middleware that just prints the method and parameters
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
