package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"whotalkie/internal/state"
	"whotalkie/internal/types"
)

// Server represents the WhoTalkie server with all its dependencies
type Server struct {
	stateManager *state.Manager
	router       *gin.Engine
	httpServer   *http.Server
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewServer creates a new server instance with proper dependency injection
func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		stateManager: state.NewManager(),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// NewServerWithOptions creates a server with custom options
func NewServerWithOptions(bufferSize int) *Server {
	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		stateManager: state.NewManagerWithBufferSize(bufferSize),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Security and performance constants
const (
	MaxAudioChunkSize = 1024 * 1024 // 1MB limit for audio chunks to prevent memory exhaustion
)

// parseChunkSizeValue parses a chunk size value from various types
func parseChunkSizeValue(chunkSizeRaw interface{}, userID string) (int64, bool) {
	switch v := chunkSizeRaw.(type) {
	case float64:
		return parseFloat64ChunkSize(v, userID)
	case string:
		return parseStringChunkSize(v, userID)
	case json.Number:
		return parseJSONNumberChunkSize(v, userID)
	default:
		log.Printf("SECURITY: Unexpected chunk size type from user %s: value=%v", userID, v)
		return 0, false
	}
}

// parseFloat64ChunkSize validates and converts float64 chunk size
func parseFloat64ChunkSize(v float64, userID string) (int64, bool) {
	if v < 0 || v > math.MaxInt64 || math.Trunc(v) != v {
		log.Printf("SECURITY: Invalid float64 chunk size rejected from user %s: %v", userID, v)
		return 0, false
	}
	return int64(v), true
}

// parseStringChunkSize parses string chunk size
func parseStringChunkSize(v string, userID string) (int64, bool) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		log.Printf("SECURITY: Invalid string chunk size from user %s: %v", userID, v)
		return 0, false
	}
	return n, true
}

// parseJSONNumberChunkSize parses json.Number chunk size
func parseJSONNumberChunkSize(v json.Number, userID string) (int64, bool) {
	n, err := v.Int64()
	if err != nil {
		log.Printf("SECURITY: Invalid json.Number chunk size from user %s: %v", userID, v)
		return 0, false
	}
	return n, true
}

// validateChunkSizeRange validates the chunk size is within allowed range
func validateChunkSizeRange(chunkSizeInt int64, userID string) bool {
	if chunkSizeInt > MaxAudioChunkSize || chunkSizeInt <= 0 {
		log.Printf("SECURITY: Invalid chunk size rejected from user %s: %v bytes", userID, chunkSizeInt)
		return false
	}
	return true
}

// validateChunkSize validates chunk size from event data with proper type handling
func validateChunkSize(eventData map[string]interface{}, userID string) (int64, bool) {
	chunkSizeRaw, ok := eventData["chunk_size"]
	if !ok {
		log.Printf("SECURITY: Missing chunk size from user %s", userID)
		return 0, false
	}

	chunkSizeInt, valid := parseChunkSizeValue(chunkSizeRaw, userID)
	if !valid {
		return 0, false
	}

	if !validateChunkSizeRange(chunkSizeInt, userID) {
		return 0, false
	}

	return chunkSizeInt, true
}

// sendMessageToClient sends a JSON message to a specific client with error handling
func sendMessageToClient(stateManager *state.Manager, userID string, message interface{}) {
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

// isValidChannelLength validates channel name length
func isValidChannelLength(channelName string) bool {
	return len(channelName) > 0 && len(channelName) <= 32
}

// isValidChannelChar validates a single character for channel names
func isValidChannelChar(char rune) bool {
	return (char >= 'a' && char <= 'z') ||
		(char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') ||
		char == '-' || char == '_' || char == '.'
}

// containsDirectoryTraversal checks for directory traversal patterns
func containsDirectoryTraversal(channelName string) bool {
	return strings.Contains(channelName, "..") ||
		strings.HasPrefix(channelName, ".") ||
		strings.HasSuffix(channelName, ".")
}

// isValidChannelName validates channel names to prevent injection attacks
func isValidChannelName(channelName string) bool {
	// Security: Channel name validation to prevent injection attacks
	if !isValidChannelLength(channelName) {
		return false
	}

	// Allow only alphanumeric characters, hyphens, underscores, and dots
	for _, char := range channelName {
		if !isValidChannelChar(char) {
			return false
		}
	}

	// Additional security: prevent directory traversal patterns
	if containsDirectoryTraversal(channelName) {
		return false
	}

	return true
}

// isValidUsernameLength validates username length
func isValidUsernameLength(username string) bool {
	return len(username) > 0 && len(username) <= 32
}

// isAlphanumeric checks if character is alphanumeric
func isAlphanumeric(char rune) bool {
	return (char >= 'a' && char <= 'z') ||
		(char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9')
}

// isSpecialUsernameChar checks if character is allowed special character
func isSpecialUsernameChar(char rune) bool {
	return char == ' ' || char == '-' || char == '_' ||
		char == '.' || char == '(' || char == ')'
}

// isValidUsernameChar validates a single character for usernames
func isValidUsernameChar(char rune) bool {
	return isAlphanumeric(char) || isSpecialUsernameChar(char)
}

// isForbiddenUsername checks if username is in forbidden list
func isForbiddenUsername(username string) bool {
	lowerUsername := strings.ToLower(strings.TrimSpace(username))
	forbiddenNames := []string{"system", "server", "admin", "root", "null", "undefined"}
	for _, forbidden := range forbiddenNames {
		if lowerUsername == forbidden {
			return true
		}
	}
	return false
}

// isValidUsername validates usernames to prevent injection attacks
func isValidUsername(username string) bool {
	// Security: Username validation to prevent injection attacks
	if !isValidUsernameLength(username) {
		return false
	}

	// Allow alphanumeric, spaces, hyphens, underscores, and common punctuation
	for _, char := range username {
		if !isValidUsernameChar(char) {
			return false
		}
	}

	// Prevent usernames that could be confused with system messages
	if isForbiddenUsername(username) {
		return false
	}

	return true
}

func (s *Server) handleWebSocket(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		// Security: Restrict origins to localhost for development
		// TODO: Configure proper origins for production deployment
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*", "http://localhost:*", "http://127.0.0.1:*"},
	})
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}

	// Create context for connection lifecycle management
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

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

	// Enhanced connection lifecycle management
	connectionManager := &ConnectionManager{
		conn:         conn,
		wsConn:       wsConn,
		user:         user,
		ctx:          ctx,
		cancel:       cancel,
		userID:       userID,
		username:     username,
		stateManager: s.stateManager,
	}

	connectionManager.initialize()
	defer connectionManager.cleanup()

	// Start goroutines with proper context
	go connectionManager.handleClientWrite()

	// Main read loop with enhanced error handling
	connectionManager.handleClientRead()
}

// ConnectionManager manages the lifecycle of a WebSocket connection
type ConnectionManager struct {
	conn         *websocket.Conn
	wsConn       *types.WebSocketConnection
	user         *types.User
	ctx          context.Context
	cancel       context.CancelFunc
	userID       string
	username     string
	stateManager *state.Manager
}

func (cm *ConnectionManager) initialize() {
	cm.stateManager.AddUser(cm.user)
	cm.stateManager.AddClient(cm.userID, cm.wsConn)

	log.Printf("New WebSocket connection: User %s (%s)", cm.username, cm.userID)

	cm.stateManager.BroadcastEvent(&types.PTTEvent{
		Type:      string(types.EventUserJoin),
		UserID:    cm.userID,
		ChannelID: "",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"username": cm.username,
		},
	})
}

func (cm *ConnectionManager) cleanup() {
	// Cancel context to stop all associated goroutines
	cm.cancel()

	// Close WebSocket connection
	closeStatus := websocket.StatusNormalClosure
	closeReason := ""

	// Check if context was cancelled due to error
	if err := cm.ctx.Err(); err != nil && err != context.Canceled {
		closeStatus = websocket.StatusInternalError
		closeReason = "Internal error"
	}

	if err := cm.conn.Close(closeStatus, closeReason); err != nil {
		log.Printf("Error closing WebSocket connection for user %s: %v", cm.userID, err)
	}

	// Clean up state
	cm.stateManager.RemoveUser(cm.userID)
	cm.stateManager.RemoveClient(cm.userID)

	// Broadcast leave event
	cm.stateManager.BroadcastEvent(&types.PTTEvent{
		Type:      string(types.EventUserLeave),
		UserID:    cm.userID,
		ChannelID: cm.user.Channel,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"username": cm.username,
			"reason":   "disconnect",
		},
	})

	log.Printf("User %s (%s) disconnected", cm.username, cm.userID)
}

func (cm *ConnectionManager) handleClientWrite() {
	for {
		select {
		case <-cm.ctx.Done():
			return
		case message, ok := <-cm.wsConn.Send:
			if !ok {
				return // Channel closed
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

			// Use context with timeout for write operations
			writeCtx, cancel := context.WithTimeout(cm.ctx, 10*time.Second)
			err := cm.conn.Write(writeCtx, messageType, message)
			cancel()

			if err != nil {
				log.Printf("WebSocket write error for user %s: %v", cm.userID, err)
				cm.cancel() // Trigger cleanup
				return
			}
		}
	}
}

func (cm *ConnectionManager) handleClientRead() {
	var expectingAudioData bool
	var audioMetadata *types.PTTEvent

	for {
		select {
		case <-cm.ctx.Done():
			return
		default:
			// Set read timeout
			readCtx, cancel := context.WithTimeout(cm.ctx, 30*time.Second)
			msgType, message, err := cm.wsConn.Conn.Read(readCtx)
			cancel()

			if err != nil {
				// Check if it's a normal close
				if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
					log.Printf("WebSocket normal close for user %s", cm.userID)
				} else {
					log.Printf("WebSocket read error for user %s: %v", cm.userID, err)
				}
				cm.cancel() // Trigger cleanup
				return
			}

			switch msgType {
			case websocket.MessageText:
				cm.handleTextMessage(message, &expectingAudioData, &audioMetadata)
			case websocket.MessageBinary:
				cm.handleBinaryMessage(message, &expectingAudioData, &audioMetadata)
			}
		}
	}
}

func (cm *ConnectionManager) handleTextMessage(message []byte, expectingAudioData *bool, audioMetadata **types.PTTEvent) {
	var event types.PTTEvent
	if err := json.Unmarshal(message, &event); err != nil {
		log.Printf("Failed to parse message from user %s: %v", cm.userID, err)
		return
	}

	event.UserID = cm.userID
	event.Timestamp = time.Now()

	// Check if this is audio metadata
	if event.Type == string(types.EventAudioData) {
		handleAudioMetadata(&event, cm.wsConn, expectingAudioData, audioMetadata)
	} else {
		handleEvent(cm.stateManager, &event)
	}
}

func (cm *ConnectionManager) handleBinaryMessage(message []byte, expectingAudioData *bool, audioMetadata **types.PTTEvent) {
	// Security: Limit binary message size to prevent memory exhaustion
	if len(message) > MaxAudioChunkSize {
		log.Printf("SECURITY: Rejecting oversized binary message from user %s: %d bytes", cm.userID, len(message))
		return
	}

	// Handle binary audio data
	if *expectingAudioData && *audioMetadata != nil {
		handleAudioData(cm.stateManager, cm.wsConn, *audioMetadata, message)
		*expectingAudioData = false
		*audioMetadata = nil
	} else {
		log.Printf("Received unexpected binary data from user %s", cm.userID)
	}
}

// handleAudioMetadata processes audio metadata events
func handleAudioMetadata(event *types.PTTEvent, wsConn *types.WebSocketConnection, expectingAudioData *bool, audioMetadata **types.PTTEvent) {
	// Security: Validate chunk size to prevent memory exhaustion
	chunkSize, valid := validateChunkSize(event.Data, wsConn.UserID)
	if !valid {
		return
	}

	*expectingAudioData = true
	*audioMetadata = event
	format := "pcm" // Default to pcm for backward compatibility
	if f, ok := event.Data["format"].(string); ok {
		format = f
	}
	log.Printf("Expecting %s audio data from user %s, size: %d bytes", format, wsConn.UserID, chunkSize)
}

// handleBinaryMessage processes binary messages from WebSocket

// sendErrorToClient sends an error event to a client
func sendErrorToClient(stateManager *state.Manager, userID string, message string, code string) {
	errorEvent := &types.PTTEvent{
		Type:      "error",
		UserID:    userID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"message": message,
			"code":    code,
		},
	}
	sendMessageToClient(stateManager, userID, errorEvent)
}

// validateUserForAudio validates if user can send audio
func validateUserForAudio(stateManager *state.Manager, wsConn *types.WebSocketConnection) (*types.User, bool) {
	user, exists := stateManager.GetUser(wsConn.UserID)
	if !exists || user.Channel == "" {
		log.Printf("Audio data from user %s but no active channel", wsConn.UserID)
		sendErrorToClient(stateManager, wsConn.UserID, "No active channel for audio transmission", "NO_CHANNEL")
		return nil, false
	}
	return user, true
}

// getAudioFormat extracts audio format from metadata
func getAudioFormat(metadata *types.PTTEvent) string {
	format := "pcm"
	if f, ok := metadata.Data["format"].(string); ok {
		format = f
	}
	return format
}

// broadcastAudioToChannel broadcasts audio data to channel users
func broadcastAudioToChannel(stateManager *state.Manager, wsConn *types.WebSocketConnection, metadata *types.PTTEvent, audioData []byte, channel *types.Channel) {
	for _, channelUser := range channel.Users {
		if shouldSkipUser(channelUser, wsConn.UserID) {
			continue
		}

		client, exists := stateManager.GetClient(channelUser.ID)
		if !exists {
			continue
		}

		sendAudioToClient(client, metadata, audioData, channelUser.ID)
	}
}

// shouldSkipUser determines if audio should be sent to a user
func shouldSkipUser(channelUser types.User, senderID string) bool {
	return channelUser.ID == senderID || channelUser.PublishOnly
}

// sendAudioToClient sends audio metadata and data to a client
func sendAudioToClient(client *types.WebSocketConnection, metadata *types.PTTEvent, audioData []byte, userID string) {
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		log.Printf("Failed to marshal audio metadata: %v", err)
		return
	}

	select {
	case client.Send <- metadataBytes:
		select {
		case client.Send <- audioData:
			// Success
		default:
			log.Printf("Audio data send buffer full for user %s", userID)
		}
	default:
		log.Printf("Metadata send buffer full for user %s", userID)
	}
}

func handleAudioData(stateManager *state.Manager, wsConn *types.WebSocketConnection, metadata *types.PTTEvent, audioData []byte) {
	user, valid := validateUserForAudio(stateManager, wsConn)
	if !valid {
		return
	}

	format := getAudioFormat(metadata)
	log.Printf("Relaying %d bytes of %s audio from user %s in channel %s",
		len(audioData), format, user.Username, user.Channel)

	// Get all clients in the same channel
	channel, exists := stateManager.GetChannel(user.Channel)
	if !exists {
		log.Printf("Channel %s not found for audio relay", user.Channel)
		return
	}

	broadcastAudioToChannel(stateManager, wsConn, metadata, audioData, channel)
}

func handleEvent(stateManager *state.Manager, event *types.PTTEvent) {
	log.Printf("Handling event: %s from user %s", event.Type, event.UserID)

	switch event.Type {
	case string(types.EventChannelJoin):
		handleChannelJoin(stateManager, event)
	case string(types.EventChannelLeave):
		handleChannelLeave(stateManager, event)
	case string(types.EventPTTStart):
		handlePTTStart(stateManager, event)
	case string(types.EventPTTEnd):
		handlePTTEnd(stateManager, event)
	case string(types.EventHeartbeat):
		handleHeartbeat(stateManager, event)
	default:
		log.Printf("Unknown event type: %s", event.Type)
	}
}

func handleChannelJoin(stateManager *state.Manager, event *types.PTTEvent) {
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

func handleChannelLeave(stateManager *state.Manager, event *types.PTTEvent) {
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

func handlePTTStart(stateManager *state.Manager, event *types.PTTEvent) {
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

func handlePTTEnd(stateManager *state.Manager, event *types.PTTEvent) {
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

func handleHeartbeat(stateManager *state.Manager, event *types.PTTEvent) {
	user, exists := stateManager.GetUser(event.UserID)
	if !exists {
		return
	}

	response := &types.PTTEvent{
		Type:      string(types.EventHeartbeat),
		UserID:    event.UserID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"status":   "pong",
			"username": user.Username,
		},
	}

	sendMessageToClient(stateManager, event.UserID, response)
}

func (s *Server) broadcastEvents() {
	// Track consecutive failures per client for cleanup
	clientFailures := make(map[string]int)
	const maxConsecutiveFailures = 5 // Clean up after 5 consecutive failures

	for {
		select {
		case event, ok := <-s.stateManager.GetEventChannel():
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

			clients := s.stateManager.GetAllClients()
			var clientsToRemove []string

			for userID, client := range clients {
				select {
				case client.Send <- eventBytes:
					// Reset failure count on successful send
					delete(clientFailures, userID)
				default:
					// Track consecutive failures
					clientFailures[userID]++
					failureCount := clientFailures[userID]

					log.Printf("Client %s send channel full (failure %d/%d)",
						userID, failureCount, maxConsecutiveFailures)

					// Mark for removal if too many failures
					if failureCount >= maxConsecutiveFailures {
						clientsToRemove = append(clientsToRemove, userID)
						log.Printf("Marking client %s for cleanup due to consecutive failures", userID)
					}
				}
			}

			// Clean up problematic clients outside the iteration
			for _, userID := range clientsToRemove {
				go s.cleanupDisconnectedClient(userID)
				delete(clientFailures, userID)
			}

		case <-s.ctx.Done():
			log.Println("Context cancelled, stopping broadcast goroutine")
			return
		}
	}
}

// cleanupDisconnectedClient removes a client that appears to be disconnected
func (s *Server) cleanupDisconnectedClient(userID string) {
	log.Printf("Cleaning up potentially disconnected client: %s", userID)

	// Remove from state manager (this will handle channel cleanup too)
	s.stateManager.RemoveClient(userID)
	s.stateManager.RemoveUser(userID)

	// Broadcast user leave event
	s.stateManager.BroadcastEvent(&types.PTTEvent{
		Type:      string(types.EventUserLeave),
		UserID:    userID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"reason": "connection_cleanup",
		},
	})
}

// setupRoutes configures all HTTP routes for the server
func (s *Server) setupRoutes() {
	s.router = gin.Default()

	s.router.Static("/static", "./web/static")
	s.router.LoadHTMLGlob("web/templates/*")

	s.router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"service": "whotalkie",
		})
	})

	s.router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "dashboard.html", gin.H{
			"title": "WhoTalkie Dashboard",
		})
	})

	s.router.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "WhoTalkie PTT Server",
			"version": "0.1.0",
		})
	})

	s.router.GET("/api/stats", s.handleStats)
	s.router.GET("/api/users", s.handleUsers)
	s.router.GET("/api/channels", s.handleChannels)
	s.router.GET("/ws", s.handleWebSocket)
}

// Handler methods for the server
func (s *Server) handleStats(c *gin.Context) {
	stats := s.stateManager.GetStats()
	c.JSON(http.StatusOK, stats)
}

func (s *Server) handleUsers(c *gin.Context) {
	users := s.stateManager.GetAllUsers()
	c.JSON(http.StatusOK, gin.H{"users": users})
}

func (s *Server) handleChannels(c *gin.Context) {
	channels := s.stateManager.GetAllChannels()
	c.JSON(http.StatusOK, gin.H{"channels": channels})
}

func main() {
	server := NewServer()
	defer server.Shutdown()

	// Start background services
	server.Start()

	// Setup routes
	server.setupRoutes()

	// Run server
	if err := server.Run(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// Start begins background services for the server
func (s *Server) Start() {
	// Start event broadcasting goroutine
	go s.broadcastEvents()
}

// Run starts the HTTP server with graceful shutdown
func (s *Server) Run() error {
	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:              ":8080",
		Handler:           s.router,
		ReadHeaderTimeout: 30 * time.Second,
	}

	// Handle graceful shutdown in a separate goroutine
	go s.handleShutdown()

	log.Println("Starting WhoTalkie server on :8080 (Ctrl+C to stop)")
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start server: %w", err)
	}

	return nil
}

// handleShutdown manages graceful server shutdown
func (s *Server) handleShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("🛑 Shutting down server...")

	// Cancel context to stop goroutines
	s.cancel()

	// Shutdown state manager and close connections
	s.stateManager.Shutdown()

	// Give active connections 30 seconds to finish
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	} else {
		log.Println("✅ Server shutdown complete")
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.stateManager != nil {
		s.stateManager.Shutdown()
	}
}
