package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"whotalkie/internal/state"
	"whotalkie/internal/types"
)

var stateManager *state.Manager

func handleWebSocket(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

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
				expectingAudioData = true
				audioMetadata = &event
				format := "pcm" // Default to pcm for backward compatibility
				if f, ok := event.Data["format"].(string); ok {
					format = f
				}
				log.Printf("Expecting %s audio data from user %s, size: %v bytes", format, wsConn.UserID, event.Data["chunk_size"])
			} else {
				handleEvent(&event)
			}
			
		case websocket.MessageBinary:
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

	// Broadcast audio to all other users in the channel
	for _, channelUser := range channel.Users {
		if channelUser.ID == wsConn.UserID {
			continue // Don't send audio back to sender
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
	
	channel := stateManager.GetOrCreateChannel(channelID, channelID)
	if err := stateManager.JoinChannel(event.UserID, channelID); err != nil {
		log.Printf("Failed to join channel %s: %v", channelID, err)
		return
	}
	
	user, _ := stateManager.GetUser(event.UserID)
	event.Data = map[string]interface{}{
		"username":     user.Username,
		"channel_name": channel.Name,
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
	
	if client, exists := stateManager.GetClient(event.UserID); exists {
		if responseBytes, err := json.Marshal(response); err == nil {
			select {
			case client.Send <- responseBytes:
			default:
			}
		}
	}
}

func broadcastEvents() {
	for event := range stateManager.GetEventChannel() {
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
	}
}

func main() {
	stateManager = state.NewManager()
	
	go broadcastEvents()
	
	r := gin.Default()

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

	log.Println("Starting WhoTalkie server on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}