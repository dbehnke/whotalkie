package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"whotalkie/pkg/protocol"
	"whotalkie/internal/state"
	"whotalkie/internal/types"
)

// TestWebSocketHandshakeIntegration starts an httptest server using the real
// handleWebSocket route and performs a websocket dial to /ws, sending a hello
// and asserting a welcome with negotiated_transmit_format is returned.
// nolint:cyclop
func TestWebSocketHandshakeIntegration(t *testing.T) {
	// Create server and router as in main
	router := gin.New()
	s := &Server{router: router, stateManager: state.NewManager()}
	router.GET("/ws", s.handleWebSocket)

	ts := httptest.NewServer(router)
	defer ts.Close()

	// Convert test server URL to ws://
	dialURL := ts.URL
	if len(dialURL) > 7 && dialURL[:7] == "http://" {
		dialURL = "ws://" + dialURL[7:]
	} else if len(dialURL) > 8 && dialURL[:8] == "https://" {
		dialURL = "wss://" + dialURL[8:]
	}

	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, dialURL+"/ws", nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") }()

	// Send a hello event over the websocket
	hello := types.PTTEvent{
		Type:      "hello",
		UserID:    "int-user-1",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"username":   "int-alice",
			"channel":    "int-room",
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

	// Read a message back - expect welcome
	ctxR, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	msgType, data, err := conn.Read(ctxR)
	if err != nil {
		t.Fatalf("failed to read welcome: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("expected text message, got type %v", msgType)
	}
	var we types.PTTEvent
	if err := json.Unmarshal(data, &we); err != nil {
		t.Fatalf("failed to unmarshal welcome: %v", err)
	}
	if we.Type != "welcome" {
		t.Fatalf("expected welcome type, got %s", we.Type)
	}
	if accepted, ok := we.Data["accepted"].(bool); !ok || !accepted {
		t.Fatalf("expected accepted=true in welcome, got %v", we.Data["accepted"])
	}
	if _, ok := we.Data[protocol.WelcomeKeyNegotiatedTransmitFormat]; !ok {
		t.Fatalf("expected negotiated_transmit_format in welcome")
	}
}
