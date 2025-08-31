package main

import (
    "context"
    "net/http/httptest"
    "testing"

    "github.com/coder/websocket"
    "github.com/gin-gonic/gin"
    "github.com/segmentio/ksuid"
    cidpkg "whotalkie/internal/cid"
)

// TestCIDPropagationIntegration is a small integration-style test that creates
// an httptest server which accepts a websocket and asserts the incoming
// connection contained the CID header previously attached to the client's
// context via cidpkg.AddHeaderFromContext.
func TestCIDPropagationIntegration(t *testing.T) {
    // Create a router that accepts websocket upgrades and captures headers.
    router := gin.New()
    var receivedCID string
    // Handler records the incoming header and returns a non-101 response.
    // websocket.Dial will observe the header even if the handshake fails.
    router.GET("/ws", func(c *gin.Context) {
        receivedCID = c.GetHeader(cidpkg.HeaderName)
        c.String(400, "no-upgrade")
    })

    ts := httptest.NewServer(router)
    defer ts.Close()

    // Build a headers map using a context containing a CID.
    ctx := context.Background()
    cid := ksuid.New().String()
    ctx = cidpkg.WithCID(ctx, cid)
    headers := map[string][]string{"User-Agent": {"integration-test/1.0"}}
    cidpkg.AddHeaderFromContext(headers, ctx)

    // Dial the server with the constructed headers. Convert http:// -> ws://
    dialURL := ts.URL
    if len(dialURL) > 7 && dialURL[:7] == "http://" {
        dialURL = "ws://" + dialURL[7:]
    } else if len(dialURL) > 8 && dialURL[:8] == "https://" {
        dialURL = "wss://" + dialURL[8:]
    }
    // We don't require a successful websocket upgrade for this test; dial may
    // return an error if server doesn't upgrade, but the headers should still
    // have been delivered to the server.
    _, _, _ = websocket.Dial(ctx, dialURL+"/ws", &websocket.DialOptions{HTTPHeader: headers})

    if receivedCID == "" {
        t.Fatalf("expected server to receive CID header %s, got empty", cidpkg.HeaderName)
    }
    if receivedCID != cid {
        t.Fatalf("expected server to receive CID %s, got %s", cid, receivedCID)
    }
}
