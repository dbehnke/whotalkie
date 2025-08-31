package main

import (
	"testing"
	"time"

	"whotalkie/internal/state"
	"whotalkie/internal/types"
)

// TestWebClientClamping ensures web clients are clamped to 32000bps and mono
func TestWebClientClamping(t *testing.T) {
	sm := state.NewManager()
	userID := "web-clamp-1"
	user := &types.User{
		ID:           userID,
		Username:     "old",
		Capabilities: types.DefaultWebClientCapabilities(),
		IsWebClient:  true,
	}
	ws := &types.WebSocketConnection{UserID: userID, Send: make(chan []byte, 4)}
	sm.AddUser(user)
	sm.AddClient(userID, ws)

	cm := &ConnectionManager{wsConn: ws, user: user, stateManager: sm, userID: userID}

	// Provide a web client asking for stereo and high bitrate
	caps := map[string]interface{}{
		"clientType": "web",
		"capabilities": map[string]interface{}{"transmit_format": map[string]interface{}{"codec": "opus", "bitrate": 200000, "sample_rate": 48000, "channels": 2}},
	}
	ev := &types.PTTEvent{Type: "hello", UserID: userID, Timestamp: time.Now(), Data: caps}

	res := cm.negotiateHello(ev)
	if !res.Accepted {
		t.Fatalf("expected negotiation to accept web client, got reject: %v", res.Extras)
	}
	if res.UpdatedUser == nil {
		t.Fatalf("expected updated user after negotiation")
	}
	tf := res.UpdatedUser.Capabilities.TransmitFormat
	if tf.Bitrate != 32000 {
		t.Fatalf("expected bitrate clamped to 32000 for web clients, got %d", tf.Bitrate)
	}
	if tf.Channels != 1 {
		t.Fatalf("expected channels clamped to 1 (mono) for web clients, got %d", tf.Channels)
	}
}
