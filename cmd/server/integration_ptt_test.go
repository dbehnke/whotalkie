package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"whotalkie/internal/types"
)

// TestWebSocketPTTIntegration verifies push-to-talk events flow between web clients:
// - two web clients connect
// - both send hello and receive welcome
// - client1 sends ptt_start, client2 receives ptt_start
// - client1 sends ptt_end, client2 receives ptt_end
func dialTestClients(t *testing.T, ts *httptest.Server) (*websocket.Conn, *websocket.Conn, context.Context) {
	// Convert URL to ws://
	dialURL := ts.URL
	if len(dialURL) > 7 && dialURL[:7] == "http://" {
		dialURL = "ws://" + dialURL[7:]
	} else if len(dialURL) > 8 && dialURL[:8] == "https://" {
		dialURL = "wss://" + dialURL[8:]
	}

	ctx := context.Background()
	c1, _, err := websocket.Dial(ctx, dialURL+"/ws", nil)
	if err != nil {
		t.Fatalf("dial client1 failed: %v", err)
	}
	deferFunc1 := func() { _ = c1.Close(websocket.StatusNormalClosure, "test done") }
	t.Cleanup(deferFunc1)

	c2, _, err := websocket.Dial(ctx, dialURL+"/ws", nil)
	if err != nil {
		_ = c1.Close(websocket.StatusNormalClosure, "test done")
		t.Fatalf("dial client2 failed: %v", err)
	}
	deferFunc2 := func() { _ = c2.Close(websocket.StatusNormalClosure, "test done") }
	t.Cleanup(deferFunc2)
	return c1, c2, ctx
}

// writeHello writes a hello event and waits until a welcome is observed.
func writeHello(t *testing.T, ctx context.Context, conn *websocket.Conn, userID, username string) {
	hello := types.PTTEvent{
		Type:      "hello",
		UserID:    userID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"username":   username,
			"channel":    "ptt-room",
			"clientType": "web",
			"capabilities": map[string]interface{}{
				"transmit_format": map[string]interface{}{"codec": "opus", "bitrate": 32000, "sample_rate": 48000, "channels": 1},
			},
		},
	}
	b, _ := json.Marshal(hello)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("failed to write hello: %v", err)
	}

	// read messages until we see a welcome
	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for welcome")
		}
		ctxR, cancel := context.WithTimeout(ctx, time.Until(deadline))
		msgType, data, err := conn.Read(ctxR)
		cancel()
		if err != nil {
			t.Fatalf("failed to read welcome: %v", err)
		}
		if msgType != websocket.MessageText {
			continue
		}
		var we types.PTTEvent
		if err := json.Unmarshal(data, &we); err != nil {
			continue
		}
		if we.Type == "welcome" {
			return
		}
	}
}

// readNextTextEvent reads the next text PTTEvent from conn within timeout.
func readNextTextEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) types.PTTEvent {
	ctxR, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	msgType, data, err := conn.Read(ctxR)
	if err != nil {
		t.Fatalf("failed to read event: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("expected text message, got %v", msgType)
	}
	var rec types.PTTEvent
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}
	return rec
}

func TestWebSocketPTTIntegration(t *testing.T) {
	router := gin.New()
	s := NewServer()
	s.router = router
	// ensure background services (broadcast goroutine) are running
	s.Start()
	router.GET("/ws", s.handleWebSocket)
	ts := httptest.NewServer(router)
	defer ts.Close()

	c1, c2, ctx := dialTestClients(t, ts)

	// perform hello for both clients
	writeHello(t, ctx, c1, "ptt-user-1", "Alice")
	writeHello(t, ctx, c2, "ptt-user-2", "Bob")

	// client1 sends ptt_start
	startEvt := types.PTTEvent{Type: string(types.EventPTTStart), UserID: "ptt-user-1", Timestamp: time.Now(), Data: map[string]interface{}{"username": "Alice"}}
	bstart, _ := json.Marshal(startEvt)
	if err := c1.Write(ctx, websocket.MessageText, bstart); err != nil {
		t.Fatalf("client1 failed to write ptt_start: %v", err)
	}

	// client2 should receive ptt_start
	rec := readNextTextEvent(t, ctx, c2)
	if rec.Type != string(types.EventPTTStart) {
		t.Fatalf("expected ptt_start, got %s", rec.Type)
	}

	// client1 sends ptt_end
	endEvt := types.PTTEvent{Type: string(types.EventPTTEnd), UserID: "ptt-user-1", Timestamp: time.Now(), Data: map[string]interface{}{"username": "Alice"}}
	bend, _ := json.Marshal(endEvt)
	if err := c1.Write(ctx, websocket.MessageText, bend); err != nil {
		t.Fatalf("client1 failed to write ptt_end: %v", err)
	}

	// client2 should receive ptt_end
	rec = readNextTextEvent(t, ctx, c2)
	if rec.Type != string(types.EventPTTEnd) {
		t.Fatalf("expected ptt_end, got %s", rec.Type)
	}
}
