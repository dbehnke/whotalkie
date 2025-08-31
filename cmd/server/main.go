package main

import (
	"context"
	"errors"
	"sync/atomic"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	zerolog "github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/segmentio/ksuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"

	"whotalkie/internal/oggdemux"
	"whotalkie/internal/stream"
	"whotalkie/internal/state"
	"whotalkie/internal/types"

	"whotalkie/pkg/protocol"

	cidpkg "whotalkie/internal/cid"
)

// Server represents the WhoTalkie server with all its dependencies
type Server struct {
	stateManager *state.Manager
	router       *gin.Engine
	httpServer   *http.Server
	ctx          context.Context
	cancel       context.CancelFunc
	tracerProvider *sdktrace.TracerProvider
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

func init() {
	// configure zerolog global logger
	level := zerolog.InfoLevel
	switch strings.ToLower(os.Getenv("WT_LOG_LEVEL")) {
	case "debug":
		level = zerolog.DebugLevel
	case "info":
		level = zerolog.InfoLevel
	case "warn", "warning":
		level = zerolog.WarnLevel
	case "error":
		level = zerolog.ErrorLevel
	case "fatal":
		level = zerolog.FatalLevel
	}
	zerolog.SetGlobalLevel(level)

	// use console writer for human-friendly logs in dev; production can set JSON via env
	// WT_LOG_JSON=1 will force structured JSON logs to stderr for production
	jsonMode := strings.ToLower(os.Getenv("WT_LOG_JSON")) == "1" || strings.ToLower(os.Getenv("WT_LOG_JSON")) == "true"
	if jsonMode {
		// default zerolog JSON output to stderr
		zlog.Logger = zlog.With().Timestamp().Logger()
		log.SetFlags(0)
		log.SetOutput(os.Stderr)
	} else {
		out := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
		zlog.Logger = zlog.Output(out)
		// Bridge stdlib log to zerolog console writer so existing log.Printf calls go through zerolog
		log.SetFlags(0)
		log.SetOutput(out)
	}
}

// WithCID and CIDFromContext are provided by the internal/cid package. Use
// cidpkg.WithCID and cidpkg.CIDFromContext.

// Security and performance constants
const (
	MaxAudioChunkSize = 1024 * 1024 // 1MB limit for audio chunks to prevent memory exhaustion
)

// MaxOpusPayloadSize is the largest Opus payload size we accept when
// constructing on-the-wire frames. This prevents attempting to encode a
// payload length that doesn't fit in a 16-bit length field.
const MaxOpusPayloadSize = 0xFFFF

// Ping/Pong supervision settings. Tests override these for speed.
var PingInterval = 20 * time.Second
var PongTimeout = 40 * time.Second
var PingWriteTimeout = 5 * time.Second
// How many consecutive ping write failures will be tolerated before closing
var PingFailureThreshold int32 = 3

// Note: negotiation codes and welcome keys moved to `pkg/protocol`

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

// detectClientType determines client type based on User-Agent and other headers
func detectClientType(userAgent string) (string, bool) {
	userAgent = strings.ToLower(userAgent)
	
	// Check for whotalkie-stream client
	if strings.Contains(userAgent, "whotalkie-stream") {
		return "whotalkie-stream", false // Not a web client
	}
	
	// Check for other known clients
	if strings.Contains(userAgent, "whotalkie-") {
		return "custom", false // Not a web client
	}
	
	// Check for web browsers
	webBrowserIndicators := []string{"mozilla", "chrome", "safari", "firefox", "edge", "opera"}
	for _, indicator := range webBrowserIndicators {
		if strings.Contains(userAgent, indicator) {
			return "web", true // Is a web client
		}
	}
	
	// Default to custom client if no browser detected
	return "custom", false
}

// validateAudioFormat validates that the client's audio format is allowed
func validateAudioFormat(user *types.User, format types.AudioFormat) error {
	if err := validateWebClientLimits(user, format); err != nil {
		return err
	}
	if err := validateBasicFormatConstraints(format); err != nil {
		return err
	}
	return nil
}

// validateWebClientLimits enforces stricter limits for web (PTT) clients.
func validateWebClientLimits(user *types.User, format types.AudioFormat) error {
	if user.IsWebClient && !user.PublishOnly {
			if format.Bitrate > 32000 {
			return &formatError{Code: protocol.CodeWebBitrateLimit, Msg: "web clients limited to 32kbps for PTT transmission"}
		}
		if format.Channels > 1 {
			return &formatError{Code: protocol.CodeWebChannelsLimit, Msg: "web clients limited to mono for PTT transmission"}
		}
	}
	return nil
}

// validateBasicFormatConstraints checks generic audio format constraints.
func validateBasicFormatConstraints(format types.AudioFormat) error {
	if format.Bitrate < 8000 || format.Bitrate > 320000 {
		return &formatError{Code: protocol.CodeBitrateOutOfRange, Msg: "bitrate must be between 8kbps and 320kbps"}
	}
	if format.Channels < 1 || format.Channels > 2 {
		return &formatError{Code: protocol.CodeChannelsOutOfRange, Msg: "channels must be 1 (mono) or 2 (stereo)"}
	}
	if format.SampleRate != 48000 && format.SampleRate != 44100 {
		return &formatError{Code: protocol.CodeSampleRateUnsupported, Msg: "sample rate must be 48000Hz or 44100Hz"}
	}
	if strings.ToLower(format.Codec) != "opus" {
		return &formatError{Code: protocol.CodeUnsupportedCodec, Msg: "only opus codec is currently supported"}
	}
	return nil
}

// formatError is a typed error used to signal specific audio format validation failures.
type formatError struct {
	Code string
	Msg  string
}

func (f *formatError) Error() string { return f.Msg }

// createDefaultCapabilities creates default capabilities based on client type
func createDefaultCapabilities(clientType string, userAgent string, isWeb bool) types.ClientCapabilities {
	switch clientType {
	case "whotalkie-stream":
		// High-quality streaming client - supports variable bitrate and stereo
		return types.ClientCapabilities{
			ClientType:   "whotalkie-stream",
			UserAgent:    userAgent,
			SupportedFormats: []types.AudioFormat{
				{Codec: "opus", Bitrate: 32000, SampleRate: 48000, Channels: 1},
				{Codec: "opus", Bitrate: 64000, SampleRate: 48000, Channels: 1},
				{Codec: "opus", Bitrate: 128000, SampleRate: 48000, Channels: 1},
				{Codec: "opus", Bitrate: 64000, SampleRate: 48000, Channels: 2},
				{Codec: "opus", Bitrate: 128000, SampleRate: 48000, Channels: 2},
			},
			TransmitFormat: types.AudioFormat{
				Codec: "opus", Bitrate: 64000, SampleRate: 48000, Channels: 2,
			},
			SupportsVariableBR: true,
			SupportsStereo:     true,
		}
	case "web":
		return types.DefaultWebClientCapabilities()
	default:
		// Custom client - assume basic capabilities
		return types.ClientCapabilities{
			ClientType:   "custom",
			UserAgent:    userAgent,
			SupportedFormats: []types.AudioFormat{
				{Codec: "opus", Bitrate: 32000, SampleRate: 48000, Channels: 1},
			},
			TransmitFormat: types.AudioFormat{
				Codec: "opus", Bitrate: 32000, SampleRate: 48000, Channels: 1,
			},
			SupportsVariableBR: false,
			SupportsStereo:     false,
		}
	}
}

func (s *Server) handleWebSocket(c *gin.Context) {
	// Prepare a lastPong holder and AcceptOptions with OnPongReceived so the
	// underlying websocket.Conn will invoke our callback when a pong arrives.
	var lastPong int64
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		// Security: Restrict origins to localhost for development
		// TODO: Configure proper origins for production deployment
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*", "http://localhost:*", "http://127.0.0.1:*"},
		OnPongReceived: func(ctx context.Context, payload []byte) {
			atomic.StoreInt64(&lastPong, time.Now().UnixNano())
		},
	})
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}

	// Create context for connection lifecycle management and reuse CID from
	// request context (set by middleware) if present; otherwise generate one.
	ctx, cancel := context.WithCancel(c.Request.Context())
	cid := cidpkg.CIDFromContext(ctx)
	if cid == "" {
		cid = ksuid.New().String()
			ctx = cidpkg.WithCID(ctx, cid)
	}
	defer cancel()

	userID := ksuid.New().String()
	username := fmt.Sprintf("User_%s", userID[:8])

	// Detect client type and capabilities from headers
	userAgent := c.GetHeader("User-Agent")
	clientType, isWebClient := detectClientType(userAgent)
	capabilities := createDefaultCapabilities(clientType, userAgent, isWebClient)

	user := &types.User{
		ID:           userID,
		Username:     username,
		Channel:      "",
		IsActive:     true,
		IsWebClient:  isWebClient,
		Capabilities: capabilities,
	}

	zlog.Info().Str("client_type", clientType).Str("username", username).Str("user_agent", userAgent).Msg("new client connected")

	wsConn := &types.WebSocketConnection{
		Conn:   conn,
		UserID: userID,
		// Larger send buffer reduces chances of transient 'send channel full'
		// situations causing the server to mark clients as failed.
		Send:   make(chan []byte, 1024),
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
		server:       s,
		cid:          cid,
		// lastPong pointer will be set just below so pingLoop can observe it
		lastPong:     nil,
		// lastAppHeartbeat will be set below and used as an alternative
		// liveness signal when application-level heartbeats are received.
		lastAppHeartbeat: nil,
	}

	// Attach the lastPong pointer used by AcceptOptions so the connection's
	// OnPongReceived callback updates the same location the pingLoop watches.
	connectionManager.lastPong = &lastPong

	// Initialize an app-heartbeat timestamp and wire it both into the
	// connection manager and the WebSocketConnection so handleHeartbeat can
	// update it via the state manager's client lookup.
	lastApp := time.Now().UnixNano()
	connectionManager.lastAppHeartbeat = &lastApp
	wsConn.LastAppHeartbeat = connectionManager.lastAppHeartbeat

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
	server       *Server
	// publisher pipe writer: write WebSocket binary frames into this to feed demux
	publisherWriter io.WriteCloser
	publisherCancel context.CancelFunc
	cid            string
	// pubMu guards publisherWriter and publisherCancel
	pubMu sync.Mutex
	// lastPong points to unix nanoseconds of the last pong received (nil if not set)
	lastPong *int64
	// lastAppHeartbeat points to the unix-nanoseconds timestamp of the
	// last application-level heartbeat received from this client. It's
	// optional and used as an additional liveness signal.
	lastAppHeartbeat *int64
	// pingFailures counts consecutive ping write failures
	pingFailures int32
}

func (cm *ConnectionManager) initialize() {
	cm.stateManager.AddUser(cm.user)
	cm.stateManager.AddClient(cm.userID, cm.wsConn)

	z := zlog.Info().Str("user", cm.username).Str("user_id", cm.userID)
	if cm.cid != "" {
		z = z.Str("cid", cm.cid)
	}
	z.Msg("New WebSocket connection")

	cm.stateManager.BroadcastEvent(&types.PTTEvent{
		Type:      string(types.EventUserJoin),
		UserID:    cm.userID,
		ChannelID: "",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"username": cm.username,
		},
	})

	// Set initial lastPong to now if pointer present
	if cm.lastPong != nil {
		atomic.StoreInt64(cm.lastPong, time.Now().UnixNano())
	}

	// Start ping supervisor goroutine
	go cm.pingLoop()
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
		z := zlog.Error().Str("user_id", cm.userID).Err(err)
		if cm.cid != "" {
			z = z.Str("cid", cm.cid)
		}
		z.Msg("Error closing WebSocket connection")
	}

	// Clean up state
	cm.stateManager.RemoveUser(cm.userID)
	cm.stateManager.RemoveClient(cm.userID)

	// Stop publisher demux if active (guarded by pubMu)
	cm.pubMu.Lock()
	if cm.publisherWriter != nil {
		_ = cm.publisherWriter.Close()
		cm.publisherWriter = nil
	}
	if cm.publisherCancel != nil {
		cm.publisherCancel()
		cm.publisherCancel = nil
	}
	cm.pubMu.Unlock()

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

	z := zlog.Info().Str("user", cm.username).Str("user_id", cm.userID)
	if cm.cid != "" {
		z = z.Str("cid", cm.cid)
	}
	z.Msg("User disconnected")
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

			// Determine message type and prepare for send
			messageType := cm.detectMessageType(message)

			// Use context with timeout for write operations
			writeCtx, cancel := context.WithTimeout(cm.ctx, 10*time.Second)
			err := cm.conn.Write(writeCtx, messageType, message)
			cancel()

			if err != nil {
				z := zlog.Error().Str("user_id", cm.userID).Err(err)
				if cm.cid != "" {
					z = z.Str("cid", cm.cid)
				}
				z.Msg("WebSocket write error")
				cm.cancel() // Trigger cleanup
				return
			}

			// Successful write - if binary, log seq and first bytes for correlation
			if messageType == websocket.MessageBinary {
				cm.logBinaryWrite(message)
			}
		}
	}
}

// pingLoop periodically sends ping frames to the client and checks for timely pongs.
func (cm *ConnectionManager) pingLoop() {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cm.ctx.Done():
			return
		case <-ticker.C:
			// Send ping with short write timeout using Conn.Ping
			pwCtx, cancel := context.WithTimeout(cm.ctx, PingWriteTimeout)
			if err := cm.conn.Ping(pwCtx); err != nil {
				cancel()
				z := zlog.Error().Str("user_id", cm.userID).Err(err)
				if cm.cid != "" {
					z = z.Str("cid", cm.cid)
				}
				z.Msg("WebSocket ping write error")

				// Increment failure counter and only close after threshold
				f := atomic.AddInt32(&cm.pingFailures, 1)
				if f >= PingFailureThreshold {
					zlog.Warn().Str("user_id", cm.userID).Int32("failures", f).Msg("ping failure threshold exceeded; closing")
					cm.cancel()
					return
				}
			} else {
				// Reset failures on success
				atomic.StoreInt32(&cm.pingFailures, 0)
			}
			cancel()

			if lastSeen, ok := cm.lastSeenTime(); ok {
				if time.Since(lastSeen) > PongTimeout {
					z := zlog.Warn().Str("user_id", cm.userID).Dur("since_last_seen", time.Since(lastSeen))
					if cm.cid != "" {
						z = z.Str("cid", cm.cid)
					}
					z.Msg("No recent pong or heartbeat; closing connection")
					cm.cancel()
					return
				}
			}
		}
	}
}

// lastSeenTime returns the most recent time seen from transport pongs or
// application-level heartbeats and a boolean indicating whether a value was
// found.
func (cm *ConnectionManager) lastSeenTime() (time.Time, bool) {
	var lastSeen time.Time
	var have bool
	if cm.lastPong != nil {
		last := time.Unix(0, atomic.LoadInt64(cm.lastPong))
		lastSeen = last
		have = true
	}
	if cm.lastAppHeartbeat != nil {
		lastApp := time.Unix(0, atomic.LoadInt64(cm.lastAppHeartbeat))
		if !have || lastApp.After(lastSeen) {
			lastSeen = lastApp
			have = true
		}
	}
	return lastSeen, have
}

// detectMessageType inspects the message bytes and returns an appropriate
// websocket.MessageType (text for JSON, binary otherwise).
func (cm *ConnectionManager) detectMessageType(message []byte) websocket.MessageType {
	var event types.PTTEvent
	if err := json.Unmarshal(message, &event); err == nil {
		return websocket.MessageText
	}
	return websocket.MessageBinary
}

// logBinaryWrite logs a brief summary of a binary message that was written to
// the connection, including sequence number and a short hex dump.
func (cm *ConnectionManager) logBinaryWrite(message []byte) {
	if len(message) >= 4 {
		seq := binary.BigEndian.Uint32(message[0:4])
		maxDump := 16
		if len(message) < maxDump {
			maxDump = len(message)
		}
		dump := ""
		if maxDump > 0 {
			dump = hex.EncodeToString(message[:maxDump])
		}
		log.Printf("WS WRITE: user=%s seq=%d first=%s", cm.userID, seq, dump)
	} else {
		log.Printf("WS WRITE: user=%s binary len=%d (no header)", cm.userID, len(message))
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
				if cm.handleReadError(err) {
					continue
				}
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

// handleReadError centralizes error handling for reads and returns true when
// the caller should continue the read loop (e.g., on deadline expired), or
// false when the caller should exit the loop. The function will call cm.cancel
// when it decides the connection must be terminated due to an error.
func (cm *ConnectionManager) handleReadError(err error) bool {
	// If the read context deadline expired, this is an idle timeout and not a
	// fatal connection error. Continue reading so idle clients are not
	// disconnected.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// If the context was cancelled, exit gracefully.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Check if it's a normal close
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		zlog.Info().Str("user_id", cm.userID).Str("cid", cm.cid).Msg("WebSocket normal close")
	} else {
		z := zlog.Error().Str("user_id", cm.userID).Err(err)
		if cm.cid != "" {
			z = z.Str("cid", cm.cid)
		}
		z.Msg("WebSocket read error")
	}
	cm.cancel() // Trigger cleanup
	return false
}

func (cm *ConnectionManager) handleTextMessage(message []byte, expectingAudioData *bool, audioMetadata **types.PTTEvent) {
	var event types.PTTEvent
	if err := json.Unmarshal(message, &event); err != nil {
		log.Printf("Failed to parse message from user %s: %v", cm.userID, err)
		return
	}

	event.UserID = cm.userID
	event.Timestamp = time.Now()

	// Handle initial hello handshake from clients
	if strings.ToLower(event.Type) == "hello" {
		cm.handleHello(&event)
		return
	}

	// Check if this is audio metadata
	if event.Type == string(types.EventAudioData) {
		handleAudioMetadata(&event, cm.wsConn, expectingAudioData, audioMetadata, cm.stateManager)

		// If publish-only and Opus, create pipe and start demux if not already created
		if cm.user != nil && cm.user.PublishOnly {
			if v, ok := event.Data["format"].(string); ok && strings.ToLower(v) == "opus" {
				if cm.publisherWriter == nil {
					pr, pw := io.Pipe()
					cm.pubMu.Lock()
					cm.publisherWriter = pw
					ctx, cancel := context.WithCancel(cm.ctx)
					cm.publisherCancel = cancel
					cm.pubMu.Unlock()
					// spawn demux goroutine
					go cm.server.startPublisherDemux(ctx, cm.user.Channel, pr)
					zlog.Info().Str("user_id", cm.userID).Str("channel", cm.user.Channel).Msg("started publisher demux")
				}
			}
		}
	} else {
		handleEvent(cm.stateManager, &event)
	}
}

// handleHello processes the initial client hello message which may include
// clientType, capabilities, preferred format, username, and channel.
func (cm *ConnectionManager) handleHello(event *types.PTTEvent) {
	// Delegate negotiation to a pure helper for easier testing
	res := cm.negotiateHello(event)

	if res.Accepted {
		if res.UpdatedUser != nil {
			cm.user = res.UpdatedUser
		}
		cm.stateManager.UpdateUser(cm.user)
		// If the hello included a channel, ensure the user is joined into that channel
		if cm.user.Channel != "" {
			// Ensure channel exists (create if needed) before joining
			cm.stateManager.GetOrCreateChannel(cm.user.Channel, cm.user.Channel)
			if err := cm.stateManager.JoinChannel(cm.userID, cm.user.Channel); err != nil {
				zlog.Warn().Err(err).Str("user_id", cm.userID).Str("channel", cm.user.Channel).Msg("failed to auto-join user to channel from hello")
			}
		}
	}

	// If accepted, include the negotiated transmit format in the welcome extras
	extras := res.Extras
	if res.Accepted {
		if extras == nil {
			extras = make(map[string]interface{})
		}
		if res.UpdatedUser != nil {
			extras[protocol.WelcomeKeyNegotiatedTransmitFormat] = res.UpdatedUser.Capabilities.TransmitFormat
		} else {
			extras[protocol.WelcomeKeyNegotiatedTransmitFormat] = cm.user.Capabilities.TransmitFormat
		}
	}

	sendWelcome(cm.stateManager, cm.userID, cm.user.Channel, res.Accepted, extras)
}

// helloResult represents the outcome of capability negotiation.
type helloResult struct {
	Accepted    bool
	Reason      string
	Extras      map[string]interface{}
	UpdatedUser *types.User
}

// negotiateHello performs validation and constructs a possibly-updated user object
// without mutating server state. It returns a helloResult describing acceptance,
// optional extras (e.g., rejection reason), and the updated user to apply.
func (cm *ConnectionManager) negotiateHello(event *types.PTTEvent) *helloResult {
	data := event.Data
	if data == nil {
		return &helloResult{Accepted: true, UpdatedUser: cm.user}
	}

	// Work on a shallow copy to avoid mutating cm.user until accepted
	userCopy := *cm.user

	// Optional username
	if err := applyUsernameFromData(&userCopy, data); err != nil {
		if fe, ok := err.(*formatError); ok {
			extras := map[string]interface{}{"reason": fe.Msg, "code": fe.Code}
			return &helloResult{Accepted: false, Reason: fe.Msg, Extras: extras}
		}
		extras := map[string]interface{}{"reason": err.Error(), "code": "invalid_username"}
		return &helloResult{Accepted: false, Reason: err.Error(), Extras: extras}
	}

	// Optional clientType and capabilities
	if err := cm.applyClientTypeFromData(&userCopy, data); err != nil {
		if fe, ok := err.(*formatError); ok {
			extras := map[string]interface{}{"reason": fe.Msg, "code": fe.Code}
			return &helloResult{Accepted: false, Reason: fe.Msg, Extras: extras}
		}
		extras := map[string]interface{}{"reason": err.Error(), "code": "bad_transmit_format"}
		return &helloResult{Accepted: false, Reason: err.Error(), Extras: extras}
	}

	// Optional channel
	if err := applyChannelFromData(&userCopy, data); err != nil {
		if fe, ok := err.(*formatError); ok {
			extras := map[string]interface{}{"reason": fe.Msg, "code": fe.Code}
			return &helloResult{Accepted: false, Reason: fe.Msg, Extras: extras}
		}
		extras := map[string]interface{}{"reason": err.Error(), "code": "invalid_channel"}
		return &helloResult{Accepted: false, Reason: err.Error(), Extras: extras}
	}

	// If capabilities were not provided, normalize defaults; otherwise they were already normalized.
	extras := map[string]interface{}{}
	if _, ok := data["capabilities"]; !ok {
		tf := userCopy.Capabilities.TransmitFormat
		newTF, adj := normalizeAudioFormat(&userCopy, tf)
		if len(adj) > 0 {
			userCopy.Capabilities.TransmitFormat = newTF
			extras["adjustments"] = adj
		}
	}

	return &helloResult{Accepted: true, UpdatedUser: &userCopy, Extras: extras}
}

// applyProvidedCapabilities decodes provided capabilities, normalizes the transmit
// format, validates it, and assigns it into userCopy.
func (cm *ConnectionManager) applyProvidedCapabilities(userCopy *types.User, caps interface{}) error {
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		return err
	}
	var newCaps types.ClientCapabilities
	if err := json.Unmarshal(capsJSON, &newCaps); err != nil {
		return err
	}
	// Normalize then validate the transmit format
	normTF, _ := normalizeAudioFormat(userCopy, newCaps.TransmitFormat)
	if err := validateAudioFormat(userCopy, normTF); err != nil {
		return err
	}
	newCaps.TransmitFormat = normTF
	userCopy.Capabilities = newCaps
	return nil
}

// applyUsernameFromData sets username on userCopy if provided and valid.
func applyUsernameFromData(userCopy *types.User, data map[string]interface{}) error {
	if uname, ok := data["username"].(string); ok && uname != "" {
		if !isValidUsername(uname) {
			return &formatError{Code: protocol.CodeInvalidUsername, Msg: "invalid username"}
		}
		userCopy.Username = uname
	}
	return nil
}

// applyChannelFromData sets channel on userCopy if provided and valid.
func applyChannelFromData(userCopy *types.User, data map[string]interface{}) error {
	if ch, ok := data["channel"].(string); ok && ch != "" {
		if !isValidChannelName(ch) {
			return &formatError{Code: protocol.CodeInvalidChannel, Msg: "invalid channel"}
		}
		userCopy.Channel = ch
	}
	return nil
}

// applyClientTypeFromData handles optional clientType and capabilities fields.
func (cm *ConnectionManager) applyClientTypeFromData(userCopy *types.User, data map[string]interface{}) error {
	ct, ok := data["clientType"].(string)
	if !ok {
		return nil
	}
	clientType := ct
	if caps, ok := data["capabilities"]; ok {
		if err := cm.applyProvidedCapabilities(userCopy, caps); err != nil {
			return err
		}
	} else {
		// No explicit capabilities; create defaults based on clientType and UA
		userAgent := userCopy.Capabilities.UserAgent
		_, isWeb := detectClientType(userAgent)
		userCopy.Capabilities = createDefaultCapabilities(clientType, userAgent, isWeb)
	}
	return nil
}

// normalizeAudioFormat adjusts an audio format to conform to server limits and returns
// the possibly-modified format along with a map describing adjustments.
func normalizeAudioFormat(user *types.User, tf types.AudioFormat) (types.AudioFormat, map[string]interface{}) {
	adjustments := map[string]interface{}{}

	tf, adj := clampBitrate(tf)
	if len(adj) > 0 {
		adjustments["bitrate"] = adj
	}

	tf, adj = clampChannels(tf)
	if len(adj) > 0 {
		adjustments["channels"] = adj
	}

	tf, adj = normalizeSampleRate(tf)
	if len(adj) > 0 {
		adjustments["sample_rate"] = adj
	}

	// For web clients, enforce stricter limits (mono, bitrate <= 32000) unless publish-only
	if user.IsWebClient && !user.PublishOnly {
		webAdj := map[string]interface{}{}
		if tf.Bitrate > 32000 {
			webAdj["web_bitrate_clamped"] = map[string]int{"from": tf.Bitrate, "to": 32000}
			tf.Bitrate = 32000
		}
		if tf.Channels > 1 {
			webAdj["web_channels_clamped"] = map[string]int{"from": tf.Channels, "to": 1}
			tf.Channels = 1
		}
		for k, v := range webAdj {
			adjustments[k] = v
		}
	}

	if len(adjustments) == 0 {
		return tf, nil
	}
	return tf, adjustments
}

func clampBitrate(tf types.AudioFormat) (types.AudioFormat, map[string]int) {
	origBR := tf.Bitrate
	if tf.Bitrate < 8000 {
		tf.Bitrate = 8000
	}
	if tf.Bitrate > 320000 {
		tf.Bitrate = 320000
	}
	if tf.Bitrate != origBR {
		return tf, map[string]int{"from": origBR, "to": tf.Bitrate}
	}
	return tf, nil
}

func clampChannels(tf types.AudioFormat) (types.AudioFormat, map[string]int) {
	origCh := tf.Channels
	if tf.Channels < 1 {
		tf.Channels = 1
	}
	if tf.Channels > 2 {
		tf.Channels = 2
	}
	if tf.Channels != origCh {
		return tf, map[string]int{"from": origCh, "to": tf.Channels}
	}
	return tf, nil
}

func normalizeSampleRate(tf types.AudioFormat) (types.AudioFormat, map[string]int) {
	origSR := tf.SampleRate
	if tf.SampleRate != 48000 && tf.SampleRate != 44100 {
		tf.SampleRate = 48000
		return tf, map[string]int{"from": origSR, "to": tf.SampleRate}
	}
	return tf, nil
}

// sendWelcome sends a welcome ack to the client. extras may include negotiated fields or rejection reason.
func sendWelcome(stateManager *state.Manager, userID, channel string, accepted bool, extras map[string]interface{}) {
	data := map[string]interface{}{
		"accepted":  accepted,
		"channel":   channel,
		"sessionId": userID,
	}
	for k, v := range extras {
		data[k] = v
	}

	welcome := &types.PTTEvent{
		Type:      "welcome",
		UserID:    userID,
		Timestamp: time.Now(),
		Data:      data,
	}
	sendMessageToClient(stateManager, userID, welcome)
}

func (cm *ConnectionManager) handleBinaryMessage(message []byte, expectingAudioData *bool, audioMetadata **types.PTTEvent) {
	// Security: Limit binary message size to prevent memory exhaustion
	if len(message) > MaxAudioChunkSize {
		log.Printf("SECURITY: Rejecting oversized binary message from user %s: %d bytes", cm.userID, len(message))
		return
	}

	// Handle binary audio data
	cm.pubMu.Lock()
	pw := cm.publisherWriter
	cm.pubMu.Unlock()
	if pw != nil {
		// write into the publisher pipe for demux
		if _, err := pw.Write(message); err != nil {
			log.Printf("Error writing binary to publisher pipe for user %s: %v", cm.userID, err)
			// close writer to signal demux EOF
			cm.pubMu.Lock()
			_ = cm.publisherWriter.Close()
			cm.publisherWriter = nil
			cm.pubMu.Unlock()
		}
		// consume expected metadata flag regardless
		if *expectingAudioData {
			*expectingAudioData = false
			*audioMetadata = nil
		}
		return
	}

	if *expectingAudioData && *audioMetadata != nil {
		handleAudioData(cm.stateManager, cm.wsConn, *audioMetadata, message)
		*expectingAudioData = false
		*audioMetadata = nil
	} else {
		log.Printf("Received unexpected binary data from user %s", cm.userID)
	}
}

// handleAudioMetadata processes audio metadata events
func handleAudioMetadata(event *types.PTTEvent, wsConn *types.WebSocketConnection, expectingAudioData *bool, audioMetadata **types.PTTEvent, stateManager *state.Manager) {
	// Security: Validate chunk size to prevent memory exhaustion
	chunkSize, valid := validateChunkSize(event.Data, wsConn.UserID)
	if !valid {
		return
	}

	// Get user to validate their audio format permissions
	user, exists := stateManager.GetUser(wsConn.UserID)
	if !exists {
		log.Printf("Audio metadata from unknown user %s", wsConn.UserID)
		sendErrorToClient(stateManager, wsConn.UserID, "User not found", "USER_NOT_FOUND")
		return
	}

	// Extract and validate audio format from metadata
	audioFormat := types.AudioFormat{
		Codec:      "opus", // Default
		Bitrate:    32000,  // Default
		SampleRate: 48000,  // Default 
		Channels:   1,      // Default
	}

	// Parse format details from metadata
	if codec, ok := event.Data["format"].(string); ok {
		audioFormat.Codec = codec
	}
	if bitrate, ok := event.Data["bitrate"].(float64); ok {
		audioFormat.Bitrate = int(bitrate)
	}
	if sampleRate, ok := event.Data["sample_rate"].(float64); ok {
		audioFormat.SampleRate = int(sampleRate)
	}
	if channels, ok := event.Data["channels"].(float64); ok {
		audioFormat.Channels = int(channels)
	}

	// Validate the audio format against user's restrictions
	if err := validateAudioFormat(user, audioFormat); err != nil {
		log.Printf("Invalid audio format from user %s: %v", wsConn.UserID, err)
		sendErrorToClient(stateManager, wsConn.UserID, fmt.Sprintf("Invalid audio format: %v", err), "INVALID_FORMAT")
		return
	}

	*expectingAudioData = true
	*audioMetadata = event

	log.Printf("Expecting %s audio from user %s: %dkbps, %dch, %dHz, size: %d bytes", 
		audioFormat.Codec, wsConn.UserID, audioFormat.Bitrate/1000, 
		audioFormat.Channels, audioFormat.SampleRate, chunkSize)

	// If this user is publish-only and is sending Ogg/Opus, the caller
	// (`ConnectionManager.handleTextMessage`) is responsible for creating the
	// publisher pipe and starting the demux goroutine. We intentionally leave
	// handling here to the caller so nothing is needed in this helper.
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

	// Enqueue metadata with visible diagnostics
	select {
	case client.Send <- metadataBytes:
		log.Printf("ENQ: metadata -> user %s (len=%d)", userID, len(metadataBytes))
	default:
		log.Printf("ENQ DROP: metadata -> user %s (buffer full)", userID)
		return
	}

	// Enqueue audio binary with diagnostics (non-blocking)
	select {
	case client.Send <- audioData:
		// Log summary of the enqueued binary (seq and first bytes hex if available)
		if len(audioData) >= 4 {
			seq := binary.BigEndian.Uint32(audioData[0:4])
			maxDump := 16
			if len(audioData) < maxDump {
				maxDump = len(audioData)
			}
			dump := ""
			if maxDump > 0 {
				dump = hex.EncodeToString(audioData[:maxDump])
			}
			log.Printf("ENQ: audio -> user %s seq=%d len=%d first=%s", userID, seq, len(audioData), dump)
		} else {
			log.Printf("ENQ: audio -> user %s len=%d (no header)", userID, len(audioData))
		}
	default:
		log.Printf("ENQ DROP: audio -> user %s (buffer full)", userID)
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
	case string(types.EventCapabilityNego):
		handleCapabilityNegotiation(stateManager, event)
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

	// Update client's app-level heartbeat timestamp if present so the
	// server's ping supervisor treats app heartbeats as evidence of
	// liveness (useful when intermediaries drop control frames).
	if client, ok := stateManager.GetClient(event.UserID); ok && client != nil && client.LastAppHeartbeat != nil {
		atomic.StoreInt64(client.LastAppHeartbeat, time.Now().UnixNano())
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

func handleCapabilityNegotiation(stateManager *state.Manager, event *types.PTTEvent) {
	user, exists := stateManager.GetUser(event.UserID)
	if !exists {
		log.Printf("Capability negotiation from unknown user %s", event.UserID)
		return
	}

	// Parse capabilities from event data
	capabilitiesData, ok := event.Data["capabilities"]
	if !ok {
		log.Printf("Missing capabilities in negotiation from user %s", event.UserID)
		sendErrorToClient(stateManager, event.UserID, "Missing capabilities data", "INVALID_NEGOTIATION")
		return
	}

	// Convert to JSON and back to properly parse the capabilities
	capabilitiesJSON, err := json.Marshal(capabilitiesData)
	if err != nil {
		log.Printf("Failed to marshal capabilities from user %s: %v", event.UserID, err)
		sendErrorToClient(stateManager, event.UserID, "Invalid capabilities format", "INVALID_NEGOTIATION")
		return
	}

	var newCapabilities types.ClientCapabilities
	if err := json.Unmarshal(capabilitiesJSON, &newCapabilities); err != nil {
		log.Printf("Failed to unmarshal capabilities from user %s: %v", event.UserID, err)
		sendErrorToClient(stateManager, event.UserID, "Invalid capabilities format", "INVALID_NEGOTIATION")
		return
	}

	// Validate the transmit format against user's restrictions
	if err := validateAudioFormat(user, newCapabilities.TransmitFormat); err != nil {
		log.Printf("Invalid transmit format from user %s: %v", event.UserID, err)
		sendErrorToClient(stateManager, event.UserID, fmt.Sprintf("Invalid transmit format: %v", err), "INVALID_FORMAT")
		return
	}

	// Update user capabilities
	user.Capabilities = newCapabilities
	stateManager.UpdateUser(user)

	log.Printf("Updated capabilities for user %s (%s): %s client, TX: %dkbps %dch %s", 
		user.Username, user.ID, newCapabilities.ClientType,
		newCapabilities.TransmitFormat.Bitrate/1000,
		newCapabilities.TransmitFormat.Channels,
		newCapabilities.TransmitFormat.Codec)

	// Send acknowledgment with server's accepted capabilities
	response := &types.PTTEvent{
		Type:      string(types.EventCapabilityNego),
		UserID:    event.UserID,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"status":       "accepted",
			"capabilities": user.Capabilities,
		},
	}

	sendMessageToClient(stateManager, event.UserID, response)
}

func (s *Server) broadcastEvents() {
	// Track consecutive failures per client for cleanup
	clientFailures := make(map[string]int)

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

			s.broadcastToClients(eventBytes, clientFailures)

		case <-s.ctx.Done():
			log.Println("Context cancelled, stopping broadcast goroutine")
			return
		}
	}
}

// broadcastToClients handles broadcasting to all clients with failure tracking
func (s *Server) broadcastToClients(eventBytes []byte, clientFailures map[string]int) {
	const maxConsecutiveFailures = 10 // Be more tolerant to transient failures

	clients := s.stateManager.GetAllClients()
	var clientsToRemove []string

	for userID, client := range clients {
		if s.sendToClient(client, eventBytes, userID) {
			// Reset failure count on successful send
			delete(clientFailures, userID)
		} else {
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
	s.cleanupFailedClients(clientsToRemove, clientFailures)
}

// sendToClient attempts to send data to a client, returns true on success
func (s *Server) sendToClient(client *types.WebSocketConnection, eventBytes []byte, userID string) bool {
	select {
	case client.Send <- eventBytes:
		return true
	default:
		return false
	}
}

// cleanupFailedClients removes clients that have exceeded failure threshold
func (s *Server) cleanupFailedClients(clientsToRemove []string, clientFailures map[string]int) {
	for _, userID := range clientsToRemove {
		go s.cleanupDisconnectedClient(userID)
		delete(clientFailures, userID)
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

	// Global middleware to ensure every HTTP request has a correlation id (CID)
	s.router.Use(s.cidMiddleware())
	// OpenTelemetry middleware (starts a span per HTTP request)
	if err := s.initOpenTelemetry(); err == nil {
		s.router.Use(s.otelMiddleware())
	} else {
		zlog.Warn().Err(err).Msg("otel init failed; continuing without tracing")
	}

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
	// HTTP proxy for raw Ogg streams per channel
	s.router.GET("/stream/:channel.ogg", func(c *gin.Context) {
		channel := c.Param("channel")
		if channel == "" {
			c.String(http.StatusBadRequest, "missing channel")
			return
		}

		// Find buffer
		b, ok := s.stateManager.GetStreamBuffer(channel)
		if !ok {
			c.String(http.StatusNotFound, "no active stream for channel")
			return
		}

		// Stream as application/ogg
		c.Header("Content-Type", "application/ogg")
	rc := b.NewReader(c.Request.Context())
	defer func() { _ = rc.Close() }()
		// Copy until client disconnects
		if _, err := io.Copy(c.Writer, rc); err != nil {
			// client disconnects are common, log at debug level
			z := zlog.Debug().Str("channel", channel).Err(err)
			if cid := cidpkg.CIDFromContext(c.Request.Context()); cid != "" {
				z = z.Str("cid", cid)
			}
			z.Msg("stream proxy copy error")
		}
	})
}

// initOpenTelemetry sets up a simple stdout exporter when WT_OTEL_STDOUT=1 is set.
// It's intentionally minimal: in production you should wire a real OTLP exporter.
func (s *Server) initOpenTelemetry() error {
	// Prefer OTLP exporter when WT_OTEL_OTLP_ENDPOINT is set
	otlpEndpoint := os.Getenv("WT_OTEL_OTLP_ENDPOINT")
	var tp *sdktrace.TracerProvider

	res, err := sdkresource.New(context.Background(), sdkresource.WithAttributes(
		semconv.ServiceNameKey.String("whotalkie"),
	))
	if err != nil {
		return err
	}

	if otlpEndpoint != "" {
		// OTLP endpoint requested; real OTLP exporter wiring should be used
		// in production. For this environment we log the request and fall
		// back to the stdout exporter as a safe default.
		zlog.Info().Str("otlp_endpoint", otlpEndpoint).Msg("OTLP endpoint requested; using stdout exporter as placeholder")
	}

	// If WT_OTEL_STDOUT=1, use stdout exporter
	if strings.ToLower(os.Getenv("WT_OTEL_STDOUT")) == "1" || otlpEndpoint != "" {
		exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return err
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
		)
	} else {
		return fmt.Errorf("no OTEL exporter configured; set WT_OTEL_STDOUT=1 or WT_OTEL_OTLP_ENDPOINT")
	}

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	s.tracerProvider = tp
	return nil
}

// otelMiddleware creates a span for each incoming HTTP request and injects
// the request context into downstream handlers.
func (s *Server) otelMiddleware() gin.HandlerFunc {
	tracer := otel.Tracer("whotalkie/server")
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		// Start a span named after method+path
		spanName := fmt.Sprintf("%s %s", c.Request.Method, c.Request.URL.Path)
		// Add semantic attributes for HTTP according to semantic conventions
		attrs := []attribute.KeyValue{
			semconv.HTTPMethodKey.String(c.Request.Method),
			semconv.HTTPTargetKey.String(c.Request.URL.Path),
		}
		ctx, span := tracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
		defer span.End()

		// Ensure propagation headers are read and set on the context
		otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(c.Request.Header))

		// attach our ctx to the request
		c.Request = c.Request.WithContext(ctx)

		// Add correlation id as a span attribute if present (from ctx or header)
	cid := cidpkg.CIDFromContext(ctx)
		if cid == "" {
		cid = c.GetHeader(cidpkg.HeaderName)
		}
		if cid != "" {
		span.SetAttributes(attribute.String(cidpkg.AttributeName, cid))
		}
		c.Next()
	}
}

// cidMiddleware returns a Gin middleware that ensures a KSUID-based CID is
// present on the request context and added to the response headers using
// the header name defined by cidpkg.HeaderName for downstream correlation/observability.
func (s *Server) cidMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		// Prefer CID already present on the context.
		cid := cidpkg.CIDFromContext(ctx)
		if cid == "" {
			// If the incoming request has the header, use that value.
			hdr := c.GetHeader(cidpkg.HeaderName)
			if hdr != "" {
				cid = hdr
				ctx = cidpkg.WithCID(ctx, cid)
				c.Request = c.Request.WithContext(ctx)
			} else {
				cid = ksuid.New().String()
				ctx = cidpkg.WithCID(ctx, cid)
				c.Request = c.Request.WithContext(ctx)
			}
		}
		// Expose CID to clients and proxies
	c.Writer.Header().Set(cidpkg.HeaderName, cid)
		c.Next()
	}
}

// startPublisherDemux spins up a demux loop reading Ogg from r and broadcasting
// framed Opus packets using server broadcast facilities. This is minimal and
// best-effort: it writes raw bytes into the per-channel stream buffer and
// uses internal/oggdemux to parse pages and emit Opus payloads as framed
// binary messages (15-byte header documented in docs/opus-protocol.md).
func (s *Server) startPublisherDemux(ctx context.Context, channelID string, r io.Reader) {
	tracer := otel.Tracer("whotalkie/server.publisher")
	// start a span for the demux lifecycle for this publisher
	spanName := fmt.Sprintf("publisher.demux %s", channelID)
	cid := cidpkg.CIDFromContext(ctx)
	attrs := []attribute.KeyValue{}
	if cid != "" {
		attrs = append(attrs, attribute.String(cidpkg.AttributeName, cid))
	}
	ctx, span := tracer.Start(ctx, spanName, trace.WithAttributes(attrs...), trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	buf := s.stateManager.GetOrCreateStreamBuffer(channelID)
	pr, pw := io.Pipe()

	// spawn writer goroutine (it will inherit ctx and should attach cid to its spans)
	s.spawnPublisherWriter(ctx, channelID, r, pw, buf)

	demux := oggdemux.New(pr)
	s.demuxLoop(ctx, channelID, demux)
}

// spawnPublisherWriter starts a goroutine that copies from the incoming reader
// into both the demux pipe and the channel stream buffer.
func (s *Server) spawnPublisherWriter(ctx context.Context, channelID string, r io.Reader, pw *io.PipeWriter, buf *stream.Buffer) {
	go func() {
		tracer := otel.Tracer("whotalkie/server.publisher")
		// create a short-lived span for the writer lifecycle
	cid := cidpkg.CIDFromContext(ctx)
		attrs := []attribute.KeyValue{}
		if cid != "" {
			attrs = append(attrs, attribute.String(cidpkg.AttributeName, cid))
		}
		_, wspan := tracer.Start(ctx, fmt.Sprintf("publisher.writer %s", channelID), trace.WithAttributes(attrs...), trace.WithSpanKind(trace.SpanKindInternal))
		defer wspan.End()
		defer func() {
			_ = pw.Close()
			zlog.Info().Str("channel", channelID).Msg("publisher pipe writer closed")
		}()
		tmp := make([]byte, 32*1024)
		wroteFirst := false
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				if !wroteFirst {
					s.handlePublisherFirstWrite(channelID, tmp[:n])
					wroteFirst = true
				}

				if _, werr := buf.WriteWithTimeout(tmp[:n], 100*time.Millisecond); werr != nil {
					zlog.Warn().Str("channel", channelID).Err(werr).Msg("publisher buffer write timeout")
				}
				if _, werr := pw.Write(tmp[:n]); werr != nil {
					z := zlog.Error().Str("channel", channelID).Err(werr)
				if cid := cidpkg.CIDFromContext(ctx); cid != "" {
						z = z.Str("cid", cid)
					}
					z.Msg("publisher pipe write error")
					// record error in span
					wspan.RecordError(werr)
					wspan.SetStatus(codes.Error, werr.Error())
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("publisher read error for %s: %v", channelID, err)
				}
				return
			}
		}
	}()
}

// handlePublisherFirstWrite logs the initial incoming bytes and saves a small
// sample to disk for debugging/diagnostics.
func (s *Server) handlePublisherFirstWrite(channelID string, data []byte) {
	n := len(data)
	maxDump := 16
	if n < maxDump {
		maxDump = n
	}
	if maxDump > 0 {
		first := hex.EncodeToString(data[:maxDump])
		log.Printf("publisher writer first write: n=%d first=%s", n, first)
	} else {
		log.Printf("publisher writer first write: n=%d (no bytes to dump)", n)
	}

	const maxSave = 4096
	saveLen := n
	if saveLen > maxSave {
		saveLen = maxSave
	}
	if saveLen == 0 {
		log.Printf("publisher incoming sample: nothing to log for channel %s", channelID)
		return
	}
	// Keep a short hex dump in logs for first-write diagnostics only.
	if saveLen > 0 {
		dump := hex.EncodeToString(data[:saveLen])
		log.Printf("publisher incoming sample (hex %d bytes): %s", saveLen, dump)
	}
}

// demuxLoop runs the demuxer's NextPage loop and handles pages/packets.
func (s *Server) demuxLoop(ctx context.Context, channelID string, demux *oggdemux.Demuxer) {
	seq := uint32(0)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
	page, err := demux.NextPage()
	if s.handleDemuxNextPageError(ctx, channelID, err) {
			return
		}

		if page != nil {
			s.debugLogDemuxPage(channelID, page)
			s.processDemuxPage(channelID, page, &seq)
		}
	}
}

func (s *Server) handleDemuxNextPageError(ctx context.Context, channelID string, err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		zlog.Info().Str("channel", channelID).Msg("demux EOF: closing demux loop")
		return true
	}
	z := zlog.Error().Str("channel", channelID).Err(err)
	if cid := cidpkg.CIDFromContext(ctx); cid != "" {
		z = z.Str("cid", cid)
	}
	z.Msg("demux error")
	time.Sleep(100 * time.Millisecond)
	return false
}

func (s *Server) debugLogDemuxPage(channelID string, page *oggdemux.Page) {
	log.Printf("demux page: channel=%s packets=%d granule=%d", channelID, len(page.Packets), page.GranulePosition)
	for i, pkt := range page.Packets {
		maxDump := 8
		if len(pkt.Data) < maxDump {
			maxDump = len(pkt.Data)
		}
		dump := ""
		if maxDump > 0 {
			dump = hex.EncodeToString(pkt.Data[:maxDump])
		}
		log.Printf(" demux pkt[%d]: opus=%v vorbisComment=%v channels=%d len=%d first=%s", i, pkt.IsOpus, pkt.IsVorbisComment, pkt.Channels, len(pkt.Data), dump)
	}
}

func (s *Server) processDemuxPage(channelID string, page *oggdemux.Page, seq *uint32) {
	for _, pkt := range page.Packets {
		s.handleDemuxPacket(channelID, seq, pkt)
	}
}

// handleDemuxPacket processes a single demuxed packet (meta or opus) and
// broadcasts events/messages to clients.
func (s *Server) handleDemuxPacket(channelID string, seq *uint32, pkt oggdemux.Packet) {
	if pkt.IsVorbisComment {
		s.emitVorbisMeta(channelID, pkt)
		return
	}
	if pkt.IsOpus {
		s.emitOpusPacket(channelID, seq, pkt)
	}
}

func (s *Server) emitVorbisMeta(channelID string, pkt oggdemux.Packet) {
	meta := &types.PTTEvent{
		Type:      "meta",
		Timestamp: time.Now(),
		ChannelID: channelID,
		Data: map[string]interface{}{
			"comments": string(pkt.Data),
		},
	}
	s.stateManager.BroadcastEvent(meta)
}

func (s *Server) emitOpusPacket(channelID string, seq *uint32, pkt oggdemux.Packet) {
	msg, ok := s.buildOpusFrame(seq, pkt)
	if !ok {
		return
	}
	s.logConstructedOpusFirstBytes(msg)
	s.stateManager.BroadcastEvent(&types.PTTEvent{
		Type:      string(types.EventAudioData),
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"binary": true,
		},
	})

	metadata := &types.PTTEvent{
		Type:      string(types.EventAudioData),
		Timestamp: time.Now(),
		ChannelID: channelID,
		Data: map[string]interface{}{
			"format": "opus",
		},
	}

	s.sendOpusToChannel(channelID, metadata, msg)
	(*seq)++
}

func (s *Server) buildOpusFrame(seq *uint32, pkt oggdemux.Packet) ([]byte, bool) {
	header := make([]byte, 15)
	binary.BigEndian.PutUint32(header[0:4], *seq)
	var ts uint64
	if pkt.GranulePos != 0 {
		ts = (pkt.GranulePos * 1000000) / 48000
	}
	binary.BigEndian.PutUint64(header[4:12], ts)
	if pkt.Channels == 0 {
		header[12] = 1
	} else {
		header[12] = byte(pkt.Channels)
	}
	payloadLen := len(pkt.Data)
	if payloadLen > MaxOpusPayloadSize {
		return nil, false
	}
	binary.BigEndian.PutUint16(header[13:15], uint16(payloadLen))
	return append(header, pkt.Data...), true
}

func (s *Server) logConstructedOpusFirstBytes(msg []byte) {
	maxDump := 16
	if len(msg) < maxDump {
		maxDump = len(msg)
	}
	if maxDump > 0 {
		dump := hex.EncodeToString(msg[:maxDump])
		log.Printf("DEBUG: constructed binary first %d bytes (hex): %s", maxDump, dump)
	}
}

func (s *Server) sendOpusToChannel(channelID string, metadata *types.PTTEvent, msg []byte) {
	channel, ok := s.stateManager.GetChannel(channelID)
	if !ok {
		return
	}
	metadataBytes, _ := json.Marshal(metadata)
	for _, chUser := range channel.Users {
		if chUser.PublishOnly {
			continue
		}
		client, exists := s.stateManager.GetClient(chUser.ID)
		if !exists {
			continue
		}
		s.logOutgoingBinaryFirstBytes(msg)
		select {
		case client.Send <- metadataBytes:
		default:
		}
		select {
		case client.Send <- msg:
		default:
		}
	}
}

func (s *Server) logOutgoingBinaryFirstBytes(msg []byte) {
	maxDump := 16
	if len(msg) < maxDump {
		maxDump = len(msg)
	}
	if maxDump > 0 {
		dump := hex.EncodeToString(msg[:maxDump])
		log.Printf("DEBUG: outgoing binary first %d bytes (hex): %s", maxDump, dump)
	}
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
		// Try to shutdown tracer provider and state cleanly before exiting
		zlog.Error().Err(err).Msg("Failed to start server; shutting down")
		server.Shutdown()
		os.Exit(1)
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

	// Attempt to flush traces now if tracerProvider is configured
	if s.tracerProvider != nil {
		if err := s.tracerProvider.Shutdown(context.Background()); err != nil {
			zlog.Error().Err(err).Msg("error flushing tracer provider during shutdown")
		} else {
			zlog.Info().Msg("tracer provider flushed on shutdown")
		}
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
	// Gracefully shutdown tracer provider if configured to flush traces
	if s.tracerProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.tracerProvider.Shutdown(ctx); err != nil {
			zlog.Error().Err(err).Msg("failed to shutdown tracer provider")
		} else {
			zlog.Info().Msg("tracer provider shutdown complete")
		}
	}
}
