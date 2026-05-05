package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentCard(t *testing.T) {
	card := agentCard("http://localhost:8090")
	require.Equal(t, "a2a-fixture-agent", card.Name)
	require.Equal(t, "http://localhost:8090", card.URL)
	require.Len(t, card.Skills, 2)
	require.Equal(t, "echo", card.Skills[0].ID)
	require.Equal(t, "reverse", card.Skills[1].ID)
	require.True(t, card.Capabilities.Streaming)
	require.True(t, card.Capabilities.PushNotifications)
}

func TestProcessMessage_Echo(t *testing.T) {
	msg := Message{
		Role:  "user",
		Parts: []Part{{Type: "text", Text: "hello world"}},
	}
	require.Equal(t, "hello world", processMessage(msg))
}

func TestProcessMessage_Reverse(t *testing.T) {
	msg := Message{
		Role:  "user",
		Parts: []Part{{Type: "text", Text: "reverse hello"}},
	}
	require.Equal(t, "olleh", processMessage(msg))
}

func TestProcessMessage_NonTextPartsIgnored(t *testing.T) {
	msg := Message{
		Role: "user",
		Parts: []Part{
			{Type: "file"},
			{Type: "text", Text: "hi"},
		},
	}
	require.Equal(t, "hi", processMessage(msg))
}

func TestTaskStore(t *testing.T) {
	store := newTaskStore()

	_, ok := store.get("missing")
	require.False(t, ok)

	task := &Task{
		ID:     "task-1",
		Status: TaskStatus{State: "completed"},
	}
	store.set(task)

	got, ok := store.get("task-1")
	require.True(t, ok)
	require.Equal(t, "completed", got.Status.State)
}

func TestTaskStore_Cancel(t *testing.T) {
	store := newTaskStore()
	store.set(&Task{ID: "task-2", Status: TaskStatus{State: "working"}})

	ok := store.cancel("task-2")
	require.True(t, ok)

	t2, _ := store.get("task-2")
	require.Equal(t, "canceled", t2.Status.State)

	require.False(t, store.cancel("nonexistent"))
}

func TestAgentCardJSON(t *testing.T) {
	card := agentCard("http://localhost:8090")
	b, err := json.Marshal(card)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(b), `"id":"echo"`))
	require.True(t, strings.Contains(string(b), `"id":"reverse"`))
}
