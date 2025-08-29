package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"whotalkie/internal/state"
	"whotalkie/internal/types"
)

var stateManager *state.Manager

// Security and performance constants
const (
	MaxAudioChunkSize = 1024 * 1024 // 1MB limit for audio chunks to prevent memory exhaustion
)

// validateChunkSize validates chunk size from event data with proper type handling
func validateChunkSize(eventData map[string]interface{}, userID string) (int64, bool) {
	chunkSizeRaw, ok := eventData["chunk_size"]
	if !ok {
		log.Printf("SECURITY: Missing chunk size from user %s", userID)
		return 0, false
	}
	
	var chunkSizeInt int64
	switch v := chunkSizeRaw.(type) {
	case float64:
		// Accept only if it's an integer value (no fractional part)
		if v != float64(int64(v)) {
			log.Printf("SECURITY: Non-integer chunk size rejected from user %s: %v", userID, v)
			return 0, false
		}
		chunkSizeInt = int64(v)
	case string:
		// Try to parse as integer
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			log.Printf("SECURITY: Invalid string chunk size from user %s: %v", userID, v)
			return 0, false
		}
		chunkSizeInt = n
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			log.Printf("SECURITY: Invalid json.Number chunk size from user %s: %v", userID, v)
			return 0, false
		}
		chunkSizeInt = n
	default:
		log.Printf("SECURITY: Unexpected chunk size type from user %s: value=%v", userID, v)
		return 0, false
	}
	
	if chunkSizeInt > MaxAudioChunkSize || chunkSizeInt <= 0 {
		log.Printf("SECURITY: Invalid chunk size rejected from user %s: %v bytes", userID, chunkSizeInt)
		return 0, false
	}
	
	return chunkSizeInt, true
}

// sendMessageToClient sends a JSON message to a specific client with error handling
func sendMessageToClient(userID string, message interface{}) {
	if client, exists := stateManager.GetClient(userID); exists {
		if messageBytes, err := json.Marshal(message); err == nil {
			select {
			case client.Send <- messageBytes:
			default:
				log.Printf("Send buffer full for user %s, message dropped", userID)
			}
		} else {
			log.Printf("Failed to marshal message for user %s: %v", userID, err)
		}
	}
}

// isValidChannelName validates channel names to prevent injection attacks
func isValidChannelName(channelName string) bool {
	// Security: Channel name validation to prevent injection attacks
	if len(channelName) == 0 || len(channelName) > 32 {
		return false
	}
	
	// Allow only alphanumeric characters, hyphens, underscores, and dots
	for _, char := range channelName {
		if !((char >= 'a' && char <= 'z') || 
			 (char >= 'A' && char <= 'Z') || 
			 (char >= '0' && char <= '9') || 
			 char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	
	// Additional security: prevent directory traversal patterns
	if strings.Contains(channelName, "..") || 
	   strings.HasPrefix(channelName, ".") || 
	   strings.HasSuffix(channelName, ".") {
		return false
	}
	
	return true
}

// isValidUsername validates usernames to prevent injection attacks
func isValidUsername(username string) bool {
	// Security: Username validation to prevent injection attacks  
	if len(username) == 0 || len(username) > 32 {
		return false
	}
	
	// Allow alphanumeric, spaces, hyphens, underscores, and common punctuation
	for _, char := range username {
		if !((char >= 'a' && char <= 'z') ||
			 (char >= 'A' && char <= 'Z') ||
			 (char >= '0' && char <= '9') ||
			 char == ' ' || char == '-' || char == '_' ||
			 char == '.' || char == '(' || char == ')') {
			return false
		}
	}
	
	// Prevent usernames that could be confused with system messages
	lowerUsername := strings.ToLower(strings.TrimSpace(username))
	forbiddenNames := []string{"system", "server", "admin", "root", "null", "undefined"}
	for _, forbidden := range forbiddenNames {
		if lowerUsername == forbidden {
			return false
		}
	}
	
	return true
}

func handleWebSocket(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		// Security: Restrict origins to localhost for development
		// TODO: Configure proper origins for production deployment
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*", "http://localhost:*", "http://127.0.0.1:*"},
	})
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}
	defer func() {
		if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
			log.Printf("Error closing WebSocket connection: %v", err)
		}
	}()

	userID := uuid.New().String()
	username := fmt.Sprintf("User_%s", userID[:8])
	
	user := &types.User{
		ID:       userID,
		Username: username,
		Channel:  "",
		IsActive: true,
	}
	
	wsConn := &types.WebSocketConnection{
		Conn:   conn,
		UserID: userID,
		Send:   make(chan []byte, 256),
	}
	
	stateManager.AddUser(user)
	stateManager.AddClient(userID, wsConn)
	
	log.Printf("New WebSocket connection: User %s (%s)", username, userID)
	
	stateManager.BroadcastEvent(&types.PTTEvent{
		Type:      string(types.EventUserJoin),
		UserID:    userID,
		ChannelID: "",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"username": username,
		},
	})
	
	go handleClientWrite(wsConn)
	
	defer func() {
		stateManager.RemoveUser(userID)
		stateManager.RemoveClient(userID)
		close(wsConn.Send)
		
		stateManager.BroadcastEvent(&types.PTTEvent{
			Type:      string(types.EventUserLeave),
			UserID:    userID,
			ChannelID: user.Channel,
			Timestamp: time.Now(),
			Data: map[string]interface{}{
				"username": username,
			},
		})
		
		log.Printf("User %s (%s) disconnected", username, userID)
	}()
	
	handleClientRead(wsConn)
}

func handleClientRead(wsConn *types.WebSocketConnection) {
	ctx := context.Background()
	var expectingAudioData bool
	var audioMetadata *types.PTTEvent
	
	for {
		msgType, message, err := wsConn.Conn.Read(ctx)
		if err != nil {
			log.Printf("WebSocket read error for user %s: %v", wsConn.UserID, err)
			break
		}
		
		switch msgType {
		case websocket.MessageText:
			// Handle JSON events
			var event types.PTTEvent
			if err := json.Unmarshal(message, &event); err != nil {
				log.Printf("Failed to parse message from user %s: %v", wsConn.UserID, err)
				continue
			}
			
			event.UserID = wsConn.UserID
			event.Timestamp = time.Now()
			
			// Check if this is audio metadata
			if event.Type == string(types.EventAudioData) {
				// Security: Validate chunk size to prevent memory exhaustion
				if chunkSize, valid := validateChunkSize(event.Data, wsConn.UserID); !valid {
					continue
				}
				
				expectingAudioData = true
				audioMetadata = &event
				format := "pcm" // Default to pcm for backward compatibility
				if f, ok := event.Data["format"].(string); ok {
					format = f
				}
				log.Printf("Expecting %s audio data from user %s, size: %d bytes", format, wsConn.UserID, chunkSize)
			} else {
				handleEvent(&event)
			}
			
		case websocket.MessageBinary:
			// Security: Limit binary message size to prevent memory exhaustion
			if len(message) > MaxAudioChunkSize {
				log.Printf("SECURITY: Rejecting oversized binary message from user %s: %d bytes", wsConn.UserID, len(message))
				continue
			}
			
			// Handle binary audio data
			if expectingAudioData && audioMetadata != nil {
				handleAudioData(wsConn, audioMetadata, message)
				expectingAudioData = false
				audioMetadata = nil
			} else {
				log.Printf("Received unexpected binary data from user %s", wsConn.UserID)
			}
		}
	}
}

func handleAudioData(wsConn *types.WebSocketConnection, metadata *types.PTTEvent, audioData []byte) {
	user, exists := stateManager.GetUser(wsConn.UserID)
	if !exists || user.Channel == "" {
		log.Printf("Audio data from user %s but no active channel", wsConn.UserID)
		// Send error feedback to client
		errorEvent := &types.PTTEvent{
			Type:      "error",
			UserID:    wsConn.UserID,
			Timestamp: time.Now(),
			Data: map[string]interface{}{
				"message": "No active channel for audio transmission",
				"code":    "NO_CHANNEL",
			},
		}
		sendMessageToClient(wsConn.UserID, errorEvent)
		return
	}

	format := "pcm"
	if f, ok := metadata.Data["format"].(string); ok {
		format = f
	}

	log.Printf("Relaying %d bytes of %s audio from user %s in channel %s",
		len(audioData), format, user.Username, user.Channel)

	// Get all clients in the same channel
	channel, exists := stateManager.GetChannel(user.Channel)
	if !exists {
		log.Printf("Channel %s not found for audio relay", user.Channel)
		return
	}

	// Broadcast audio to all other users in the channel (except publish-only clients)
	for _, channelUser := range channel.Users {
		if channelUser.ID == wsConn.UserID {
			continue // Don't send audio back to sender
		}
		if channelUser.PublishOnly {
			continue // Don't send audio to publish-only clients
		}

		client, exists := stateManager.GetClient(channelUser.ID)
		if !exists {
			continue
		}

		// Send audio metadata first, then binary data
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			log.Printf("Failed to marshal audio metadata: %v", err)
			continue
		}

		select {
		case client.Send <- metadataBytes:
			// Then send the binary audio data
			select {
			case client.Send <- audioData:
				// Success
			default:
				log.Printf("Audio data send buffer full for user %s", channelUser.ID)
			}
		default:
			log.Printf("Metadata send buffer full for user %s", channelUser.ID)
		}
	}
}

func handleClientWrite(wsConn *types.WebSocketConnection) {
	ctx := context.Background()
	
	for {
		select {
		case message, ok := <-wsConn.Send:
			if !ok {
				return
			}
			
			// Determine message type by trying to parse as JSON
			var messageType websocket.MessageType
			var event types.PTTEvent
			if err := json.Unmarshal(message, &event); err == nil {
				// It's JSON, send as text
				messageType = websocket.MessageText
			} else {
				// It's binary data, send as binary
				messageType = websocket.MessageBinary
			}
			
			if err := wsConn.Conn.Write(ctx, messageType, message); err != nil {
				log.Printf("WebSocket write error for user %s: %v", wsConn.UserID, err)
				return
			}
		}
	}
}

func handleEvent(event *types.PTTEvent) {
	log.Printf("Handling event: %s from user %s", event.Type, event.UserID)
	
	switch event.Type {
	case string(types.EventChannelJoin):
		handleChannelJoin(event)
	case string(types.EventChannelLeave):
		handleChannelLeave(event)
	case string(types.EventPTTStart):
		handlePTTStart(event)
	case string(types.EventPTTEnd):
		handlePTTEnd(event)
	case string(types.EventHeartbeat):
		handleHeartbeat(event)
	default:
		log.Printf("Unknown event type: %s", event.Type)
	}
}

func handleChannelJoin(event *types.PTTEvent) {
	channelID := event.ChannelID
	if channelID == "" {
		if channelName, ok := event.Data["channel_name"].(string); ok {
			channelID = channelName
		} else {
			channelID = "general"
		}
		event.ChannelID = channelID
	}
	
	// Security: Validate channel ID to prevent injection attacks
	if !isValidChannelName(channelID) {
		log.Printf("SECURITY: Invalid channel name rejected from user %s: %s", event.UserID, channelID)
		return
	}
	
	channel := stateManager.GetOrCreateChannel(channelID, channelID)
	
	user, _ := stateManager.GetUser(event.UserID)
	
	// Handle publish-only mode
	if publishOnly, ok := event.Data["publish_only"].(bool); ok && publishOnly {
		user.PublishOnly = true
		log.Printf("User %s (%s) set to publish-only mode", user.Username, user.ID)
	}
	
	// Update username if provided
	if username, ok := event.Data["username"].(string); ok && username != "" {
		// Security: Validate username to prevent injection attacks
		if isValidUsername(username) {
			user.Username = username
		} else {
			log.Printf("SECURITY: Invalid username rejected from user %s: %s", event.UserID, username)
		}
	}
	
	// Update user in state
	stateManager.UpdateUser(user)
	
	// Now join channel with updated user data
	if err := stateManager.JoinChannel(event.UserID, channelID); err != nil {
		log.Printf("Failed to join channel %s: %v", channelID, err)
		return
	}
	
	event.Data = map[string]interface{}{
		"username":     user.Username,
		"channel_name": channel.Name,
		"publish_only": user.PublishOnly,
	}
	
	stateManager.BroadcastEvent(event)
}

func handleChannelLeave(event *types.PTTEvent) {
	if err := stateManager.LeaveChannel(event.UserID, event.ChannelID); err != nil {
		log.Printf("Failed to leave channel %s: %v", event.ChannelID, err)
		return
	}
	
	user, _ := stateManager.GetUser(event.UserID)
	event.Data = map[string]interface{}{
		"username": user.Username,
	}
	
	stateManager.BroadcastEvent(event)
}

func handlePTTStart(event *types.PTTEvent) {
	user, exists := stateManager.GetUser(event.UserID)
	if !exists || user.Channel == "" {
		return
	}

	// Get the channel
	channel, exists := stateManager.GetChannel(user.Channel)
	if !exists {
		return
	}

	// BROADCAST MODE: When publish-only clients (streaming bots) are present,
	// normal users become listen-only to maintain broadcast/monitoring paradigm.
	// This enables use cases like live radio, security monitoring, conference playback.
	// See README.md "Channel Behavior & Broadcast Mode" for full documentation.
	if !user.PublishOnly && channel.PublishOnlyCount > 0 {
		log.Printf("PTT blocked for user %s in channel %s because %d publish-only user(s) present (broadcast mode)", 
			user.Username, channel.ID, channel.PublishOnlyCount)
		return
	}

	// Add user to active speakers
	if channel.ActiveSpeakers == nil {
		channel.ActiveSpeakers = make(map[string]types.SpeakerState)
	}
	channel.ActiveSpeakers[user.ID] = types.SpeakerState{
		UserID:    user.ID,
		Username:  user.Username,
		IsTalking: true,
		StartTime: time.Now(),
	}

	event.ChannelID = user.Channel
	event.Data = map[string]interface{}{
		"username": user.Username,
	}

	stateManager.BroadcastEvent(event)
}

func handlePTTEnd(event *types.PTTEvent) {
	user, exists := stateManager.GetUser(event.UserID)
	if !exists || user.Channel == "" {
		return
	}
	
	// Remove user from active speakers
	channel, exists := stateManager.GetChannel(user.Channel)
	if exists {
		delete(channel.ActiveSpeakers, user.ID)
	}
	
	event.ChannelID = user.Channel
	event.Data = map[string]interface{}{
		"username": user.Username,
	}
	
	stateManager.BroadcastEvent(event)
}

func handleHeartbeat(event *types.PTTEvent) {
	user, exists := stateManager.GetUser(event.UserID)
	if !exists {
		return
	}
	
	response := &types.PTTEvent{
		Type:      string(types.EventHeartbeat),
		UserID:    event.UserID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"status": "pong",
			"username": user.Username,
		},
	}
	
	sendMessageToClient(event.UserID, response)
}

func broadcastEvents(ctx context.Context) {
	for {
		select {
		case event, ok := <-stateManager.GetEventChannel():
			if !ok {
				// Channel closed, exit gracefully
				log.Println("Event channel closed, stopping broadcast goroutine")
				return
			}
			
			eventBytes, err := json.Marshal(event)
			if err != nil {
				log.Printf("Failed to marshal event: %v", err)
				continue
			}
			
			clients := stateManager.GetAllClients()
			for _, client := range clients {
				select {
				case client.Send <- eventBytes:
				default:
					log.Printf("Client %s send channel full, skipping", client.UserID)
				}
			}
		case <-ctx.Done():
			log.Println("Context cancelled, stopping broadcast goroutine")
			return
		}
	}
}

func main() {
	stateManager = state.NewManager()
	
	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go broadcastEvents(ctx)
	
	r := gin.Default()

	r.Static("/static", "./web/static")
	r.LoadHTMLGlob("web/templates/*")

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"service": "whotalkie",
		})
	})

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "dashboard.html", gin.H{
			"title": "WhoTalkie Dashboard",
		})
	})

	r.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "WhoTalkie PTT Server",
			"version": "0.1.0",
		})
	})
	
	r.GET("/api/stats", func(c *gin.Context) {
		stats := stateManager.GetStats()
		c.JSON(http.StatusOK, stats)
	})
	
	r.GET("/api/users", func(c *gin.Context) {
		users := stateManager.GetAllUsers()
		c.JSON(http.StatusOK, gin.H{"users": users})
	})
	
	r.GET("/api/channels", func(c *gin.Context) {
		channels := stateManager.GetAllChannels()
		c.JSON(http.StatusOK, gin.H{"channels": channels})
	})

	r.GET("/ws", handleWebSocket)

	// Create HTTP server with graceful shutdown
	server := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}

	// Handle graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		
		log.Println("🛑 Shutting down server...")
		
		// Cancel broadcast context first to stop goroutines
		cancel()
		
		// Shutdown state manager and close connections
		stateManager.Shutdown()
		
		// Give active connections 30 seconds to finish
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Server forced to shutdown: %v", err)
		} else {
			log.Println("✅ Server shutdown complete")
		}
	}()

	log.Println("Starting WhoTalkie server on :8080 (Ctrl+C to stop)")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("Failed to start server:", err)
	}
}