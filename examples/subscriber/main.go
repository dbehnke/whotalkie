package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"whotalkie/internal/otelutil"

	"github.com/coder/websocket"
)

type PTTEvent struct {
	Type      string                 `json:"type"`
	ChannelID string                 `json:"channel_id,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

func main() {
	_ = otelutil.Init()
	defer otelutil.Flush()
	ctx := context.Background()
	conn := setupSubscriber(ctx)
	done := make(chan struct{})
	go subscriberListen(ctx, conn, done)

	// Wait for signal or done
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	select {
	case <-sig:
		fmt.Println("interrupted")
	case <-done:
		fmt.Println("connection closed")
	case <-time.After(30 * time.Second):
		fmt.Println("timeout")
	}
}

func setupSubscriber(ctx context.Context) *websocket.Conn {
	server := "ws://localhost:8080/ws"
	channel := "ogg"
	username := "SubTest"

	conn, _, err := websocket.Dial(ctx, server, nil)
	if err != nil {
		log.Fatalf("failed to dial server: %v", err)
	}
	// nolint:errcheck
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "bye") }()

	cap := map[string]interface{}{"capabilities": map[string]interface{}{"receive_only": true}}
	evt := PTTEvent{Type: "capability_negotiation", Data: cap}
	sendJSON(ctx, conn, evt)

	join := PTTEvent{Type: "channel_join", ChannelID: channel, Data: map[string]interface{}{"username": username, "publish_only": false}}
	sendJSON(ctx, conn, join)
	return conn
}

func subscriberListen(ctx context.Context, conn *websocket.Conn, done chan struct{}) {
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("read error: %v", err)
			close(done)
			return
		}
		if msgType == websocket.MessageText {
			var ev PTTEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				log.Printf("text unmarshal err: %v", err)
				continue
			}
			log.Printf("event: %s data=%v", ev.Type, ev.Data)
		} else if msgType == websocket.MessageBinary {
			if len(data) < 15 {
				log.Printf("binary too short: %d bytes", len(data))
				continue
			}
			seq := binary.BigEndian.Uint32(data[0:4])
			ts := binary.BigEndian.Uint64(data[4:12])
			channels := uint8(data[12])
			payloadLen := binary.BigEndian.Uint16(data[13:15])
			if int(payloadLen) != len(data[15:]) {
				log.Printf("payload length mismatch: header=%d actual=%d", payloadLen, len(data[15:]))
			}
			// Log header summary and a small hex dump of the first bytes for comparison with server
			log.Printf("BIN hdr seq=%d ts_us=%d channels=%d payload=%d bytes", seq, ts, channels, payloadLen)
			dumpLen := 24
			if dumpLen > len(data) {
				dumpLen = len(data)
			}
			log.Printf("BIN raw first %d bytes: %s", dumpLen, hex.EncodeToString(data[:dumpLen]))
		}
	}
}

func sendJSON(ctx context.Context, conn *websocket.Conn, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("failed to marshal json for send: %v", err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		log.Fatalf("failed to send json: %v", err)
	}
}
