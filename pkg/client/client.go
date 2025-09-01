package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/coder/websocket"
	"github.com/segmentio/ksuid"

	cidpkg "whotalkie/internal/cid"
)

// buildDialHeaders constructs the HTTP header map used for websocket.Dial.
// Extracted to allow unit testing of header propagation.
func buildDialHeaders(ctx context.Context, userAgent string) map[string][]string {
	headers := map[string][]string{"User-Agent": {userAgent}}
	cidpkg.AddHeaderFromContext(headers, ctx)
	return headers
}

// StreamClient represents a WhoTalkie streaming client
type StreamClient struct {
	conn         *websocket.Conn
	userID       string
	config       ClientConfig
	capabilities ClientCapabilities
	connected    bool
	eventHandler EventHandler
}

// EventHandler defines callbacks for handling server events
type EventHandler interface {
	OnConnected()
	OnDisconnected()
	OnCapabilityNegotiated(accepted bool)
	OnChannelJoined(channel string)
	OnPTTStart(username string)
	OnPTTEnd(username string)
	OnUserJoin(username string)
	OnUserLeave(username string)
	OnError(message, code string)
	OnServerEvent(eventType string, data map[string]interface{})
}

// DefaultEventHandler provides a basic implementation of EventHandler
type DefaultEventHandler struct{}

func (h *DefaultEventHandler) OnConnected()                                            { log.Printf("Connected to server") }
func (h *DefaultEventHandler) OnDisconnected()                                        { log.Printf("Disconnected from server") }
func (h *DefaultEventHandler) OnCapabilityNegotiated(accepted bool)                   { log.Printf("Capability negotiation: accepted=%v", accepted) }
func (h *DefaultEventHandler) OnChannelJoined(channel string)                         { log.Printf("Joined channel: %s", channel) }
func (h *DefaultEventHandler) OnPTTStart(username string)                             { log.Printf("🎙️ %s started talking", username) }
func (h *DefaultEventHandler) OnPTTEnd(username string)                               { log.Printf("🔇 %s stopped talking", username) }
func (h *DefaultEventHandler) OnUserJoin(username string)                             { log.Printf("👋 %s joined", username) }
func (h *DefaultEventHandler) OnUserLeave(username string)                            { log.Printf("👋 %s left", username) }
func (h *DefaultEventHandler) OnError(message, code string)                           { log.Printf("❌ Server error [%s]: %s", code, message) }
func (h *DefaultEventHandler) OnServerEvent(eventType string, data map[string]interface{}) { log.Printf("📥 Event: %s", eventType) }

// NewStreamClient creates a new streaming client
func NewStreamClient(config ClientConfig) *StreamClient {
	userID := ksuid.New().String()
	
	// Set default user agent if not provided
	if config.UserAgent == "" {
		config.UserAgent = "whotalkie-stream/1.0.0"
	}

	capabilities := CreateHighQualityCapabilities(
		"whotalkie-stream",
		config.UserAgent,
		config.Bitrate,
		config.Channels,
	)

	return &StreamClient{
		userID:       userID,
		config:       config,
		capabilities: capabilities,
		eventHandler: &DefaultEventHandler{},
	}
}

// SetEventHandler sets a custom event handler
func (c *StreamClient) SetEventHandler(handler EventHandler) {
	c.eventHandler = handler
}

// GetUserID returns the client's user ID
func (c *StreamClient) GetUserID() string {
	return c.userID
}

// GetCapabilities returns the client's capabilities
func (c *StreamClient) GetCapabilities() ClientCapabilities {
	return c.capabilities
}

// IsConnected returns whether the client is connected
func (c *StreamClient) IsConnected() bool {
	return c.connected
}

// Connect establishes a WebSocket connection to the server
func (c *StreamClient) Connect(ctx context.Context) error {
	headers := map[string][]string{
		"User-Agent": {c.config.UserAgent},
	}
	// propagate CID from context if present (centralized helper)
	cidpkg.AddHeaderFromContext(headers, ctx)

	conn, _, err := websocket.Dial(ctx, c.config.ServerURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	
	c.conn = conn
	c.connected = true
	c.eventHandler.OnConnected()
	return nil
}

// Disconnect closes the WebSocket connection
func (c *StreamClient) Disconnect() error {
	if c.conn != nil {
		c.connected = false
		err := c.conn.Close(websocket.StatusNormalClosure, "client disconnect")
		c.eventHandler.OnDisconnected()
		return err
	}
	return nil
}

// NegotiateCapabilities sends capability negotiation to the server
func (c *StreamClient) NegotiateCapabilities(ctx context.Context) error {
	event := PTTEvent{
		Type:      "capability_negotiation",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"capabilities": c.capabilities,
		},
	}

	if err := c.sendEvent(ctx, event); err != nil {
		return fmt.Errorf("failed to send capability negotiation: %w", err)
	}

	return nil
}

// JoinChannel joins a channel on the server
func (c *StreamClient) JoinChannel(ctx context.Context) error {
	event := PTTEvent{
		Type:      "channel_join",
		ChannelID: c.config.Channel,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"channel_name": c.config.Channel,
			"username":     c.config.Username,
			"publish_only": c.config.PublishOnly,
		},
	}

	if err := c.sendEvent(ctx, event); err != nil {
		return fmt.Errorf("failed to join channel: %w", err)
	}

	return nil
}

// StartTransmission starts PTT transmission
func (c *StreamClient) StartTransmission(ctx context.Context) error {
	event := PTTEvent{
		Type:      "ptt_start",
		Timestamp: time.Now(),
	}

	if err := c.sendEvent(ctx, event); err != nil {
		return fmt.Errorf("failed to start PTT: %w", err)
	}

	return nil
}

// SendAudioData sends audio metadata and binary data
func (c *StreamClient) SendAudioData(ctx context.Context, audioData []byte) error {
	// Send audio metadata
	audioEvent := PTTEvent{
		Type:      "audio_data",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"chunk_size":   len(audioData),
			"format":       c.capabilities.TransmitFormat.Codec,
			"bitrate":      c.capabilities.TransmitFormat.Bitrate,
			"sample_rate":  c.capabilities.TransmitFormat.SampleRate,
			"channels":     c.capabilities.TransmitFormat.Channels,
		},
	}

	if err := c.sendEvent(ctx, audioEvent); err != nil {
		return fmt.Errorf("failed to send audio metadata: %w", err)
	}

	// Send binary audio data
	if err := c.conn.Write(ctx, websocket.MessageBinary, audioData); err != nil {
		return fmt.Errorf("failed to send audio data: %w", err)
	}

	return nil
}

// SendMeta sends a `meta` event (e.g., Vorbis comments) to the server for the
// current channel. This can be used by streamers to update stream title/metadata.
func (c *StreamClient) SendMeta(ctx context.Context, comments string) error {
	event := PTTEvent{
		Type:      "meta",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"comments": comments,
		},
	}

	if err := c.sendEvent(ctx, event); err != nil {
		return fmt.Errorf("failed to send meta event: %w", err)
	}
	return nil
}

// StopTransmission stops PTT transmission
func (c *StreamClient) StopTransmission(ctx context.Context) error {
	event := PTTEvent{
		Type:      "ptt_end",
		Timestamp: time.Now(),
	}

	if err := c.sendEvent(ctx, event); err != nil {
		return fmt.Errorf("failed to stop PTT: %w", err)
	}

	return nil
}

// ListenForMessages starts listening for server messages (blocking)
func (c *StreamClient) ListenForMessages(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msgType, data, err := c.conn.Read(ctx)
			if err != nil {
				c.connected = false
				return fmt.Errorf("read error: %w", err)
			}

			if msgType == websocket.MessageText {
				var event PTTEvent
				if err := json.Unmarshal(data, &event); err != nil {
					log.Printf("Failed to unmarshal message: %v", err)
					continue
				}
				c.handleServerEvent(event)
			} else if msgType == websocket.MessageBinary {
				// Handle received audio data if needed
				log.Printf("Received binary data: %d bytes", len(data))
			}
		}
	}
}

// sendEvent sends a JSON event to the server
func (c *StreamClient) sendEvent(ctx context.Context, event PTTEvent) error {
	if !c.connected {
		return fmt.Errorf("client not connected")
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	return c.conn.Write(ctx, websocket.MessageText, data)
}

// handleServerEvent processes events received from the server
func (c *StreamClient) handleServerEvent(event PTTEvent) {
	switch event.Type {
	case "capability_negotiation":
		c.processCapabilityEvent(event)
	case "channel_join":
		c.processChannelJoinEvent(event)
	case "ptt_start":
		c.processUserNameEvent(event, c.eventHandler.OnPTTStart)
	case "ptt_end":
		c.processUserNameEvent(event, c.eventHandler.OnPTTEnd)
	case "user_join":
		c.processUserNameEvent(event, c.eventHandler.OnUserJoin)
	case "user_leave":
		c.processUserNameEvent(event, c.eventHandler.OnUserLeave)
	case "error":
		c.processErrorEvent(event)
	default:
		c.eventHandler.OnServerEvent(event.Type, event.Data)
	}
}

func (c *StreamClient) processCapabilityEvent(event PTTEvent) {
	if status, ok := event.Data["status"].(string); ok && status == "accepted" {
		c.eventHandler.OnCapabilityNegotiated(true)
	} else {
		c.eventHandler.OnCapabilityNegotiated(false)
	}
}

func (c *StreamClient) processChannelJoinEvent(event PTTEvent) {
	if channelName, ok := event.Data["channel_name"].(string); ok {
		c.eventHandler.OnChannelJoined(channelName)
	}
}

// processUserNameEvent is a small helper to extract a username and call the
// provided callback (used for ptt_start, ptt_end, user_join, user_leave).
func (c *StreamClient) processUserNameEvent(event PTTEvent, cb func(string)) {
	if username, ok := event.Data["username"].(string); ok {
		cb(username)
	}
}

func (c *StreamClient) processErrorEvent(event PTTEvent) {
	message := ""
	code := ""
	if m, ok := event.Data["message"].(string); ok {
		message = m
	}
	if cc, ok := event.Data["code"].(string); ok {
		code = cc
	}
	c.eventHandler.OnError(message, code)
}