package main

import (
	"context"
	"log"
	"time"

	"whotalkie/internal/otelutil"
	"whotalkie/pkg/client"
)

func main() {
	// Initialize optional tracing (no-op unless WT_OTEL_STDOUT=1)
	_ = otelutil.Init()
	defer otelutil.Flush()

	// Configure the client
	config := client.ClientConfig{
		ServerURL:   "ws://localhost:8080/ws",
		Username:    "SimpleBot",
		Channel:     "general",
		Bitrate:     64000,  // 64kbps
		Channels:    2,      // Stereo
		PublishOnly: true,
		UserAgent:   "simple-example/1.0.0",
	}

	// Create streaming client
	streamClient := client.NewStreamingClient(config)
	
	// Use custom event handler
	streamClient.SetEventHandler(&CustomEventHandler{})

	ctx := context.Background()

	// Connect and setup (capability negotiation + channel join)
	log.Printf("🔗 Connecting to server...")
	if err := streamClient.ConnectAndSetup(ctx); err != nil {
		log.Fatalf("❌ Failed to connect: %v", err)
	}
	defer func() { _ = streamClient.Disconnect() }()

	log.Printf("📡 Connected successfully!")

	// Listen for server messages in background
	go func() {
		if err := streamClient.ListenForMessages(ctx); err != nil {
			log.Printf("📥 Message listener stopped: %v", err)
		}
	}()

	// Stream test audio for 10 seconds
	log.Printf("🎤 Starting 10-second stream...")
	duration := 10 * time.Second
	interval := 1 * time.Second
	chunkSize := 1024

	if err := streamClient.StreamForDuration(ctx, duration, interval, chunkSize); err != nil {
		log.Printf("❌ Stream error: %v", err)
	} else {
		log.Printf("✅ Stream completed successfully")
	}

	// Give time for final events
	time.Sleep(1 * time.Second)
}

// CustomEventHandler demonstrates custom event handling
type CustomEventHandler struct{}

func (h *CustomEventHandler) OnConnected() {
	log.Printf("🎉 Connected to WhoTalkie server!")
}

func (h *CustomEventHandler) OnDisconnected() {
	log.Printf("👋 Disconnected from server")
}

func (h *CustomEventHandler) OnCapabilityNegotiated(accepted bool) {
	if accepted {
		log.Printf("✅ Server accepted our audio capabilities")
	} else {
		log.Printf("❌ Server rejected our capabilities")
	}
}

func (h *CustomEventHandler) OnChannelJoined(channel string) {
	log.Printf("📺 Successfully joined channel: %s", channel)
}

func (h *CustomEventHandler) OnPTTStart(username string) {
	log.Printf("🎙️ %s started talking", username)
}

func (h *CustomEventHandler) OnPTTEnd(username string) {
	log.Printf("🔇 %s stopped talking", username)
}

func (h *CustomEventHandler) OnUserJoin(username string) {
	log.Printf("👤 %s joined the channel", username)
}

func (h *CustomEventHandler) OnUserLeave(username string) {
	log.Printf("🚪 %s left the channel", username)
}

func (h *CustomEventHandler) OnError(message, code string) {
	log.Printf("⚠️ Server error [%s]: %s", code, message)
}

func (h *CustomEventHandler) OnServerEvent(eventType string, data map[string]interface{}) {
	log.Printf("📨 Server event: %s - %v", eventType, data)
}