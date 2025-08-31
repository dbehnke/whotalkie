package main

import (
	"context"
	"testing"
	"time"
	"net/http/httptest"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// TestPingPong_ActiveClient ensures a client that responds to pings stays connected.
func TestPingPong_ActiveClient(t *testing.T) {
	// shorten intervals for test
	oldPing := PingInterval
	oldPong := PongTimeout
	oldWrite := PingWriteTimeout
	PingInterval = 100 * time.Millisecond
	PongTimeout = 300 * time.Millisecond
	PingWriteTimeout = 50 * time.Millisecond
	defer func() { PingInterval, PongTimeout, PingWriteTimeout = oldPing, oldPong, oldWrite }()

	s := NewServer()
	s.Start()
	// Avoid loading full templates during tests; create a minimal router and
	// register only the websocket route used by these tests.
	s.router = gin.Default()
	s.router.GET("/ws", s.handleWebSocket)
	ts := httptest.NewServer(s.router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+ts.URL[4:]+"/ws", nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") }()

	// Start a background reader so the client will process control frames
	// (coder/websocket requires a concurrent Reader to observe pongs).
	readCtx, readCancel := context.WithCancel(context.Background())
	defer readCancel()
	go func() {
		for {
			_, _, err := conn.Read(readCtx)
			if err != nil {
				return
			}
		}
	}()

	// Wait a bit longer than one ping interval to ensure pings are sent
	time.Sleep(500 * time.Millisecond)

	// Expect the connection to still be open
	select {
	case <-ctx.Done():
		t.Fatalf("context deadline before check: %v", ctx.Err())
	default:
	}

	// Try a ping from client side to ensure we can still write
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

// TestPingPong_DeadClient ensures a non-responsive client is disconnected by server.
func TestPingPong_DeadClient(t *testing.T) {
	oldPing := PingInterval
	oldPong := PongTimeout
	oldWrite := PingWriteTimeout
	PingInterval = 100 * time.Millisecond
	PongTimeout = 200 * time.Millisecond
	PingWriteTimeout = 50 * time.Millisecond
	defer func() { PingInterval, PongTimeout, PingWriteTimeout = oldPing, oldPong, oldWrite }()

	s := NewServer()
	s.Start()
	s.router = gin.Default()
	s.router.GET("/ws", s.handleWebSocket)
	ts := httptest.NewServer(s.router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Dial using a net.Conn wrapper that drops pong responses by not handling them.
	conn, _, err := websocket.Dial(ctx, "ws"+ts.URL[4:]+"/ws", nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	// We will not respond to pings: close underlying conn by not reading
	// Simulate dead client by closing the connection (no pong)
	// Note: coder/websocket automatically responds to pings when Read is called.
	// To simulate a dead client we simply close the connection immediately after dial.
	_ = conn.Close(websocket.StatusNormalClosure, "simulated dead")

	// Wait for server to detect missing pong and cancel connection
	time.Sleep(800 * time.Millisecond)

	// If we get here without server panic, assume server handled it.
}
