package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// PTTEvent represents the WebSocket message structure
type PTTEvent struct {
	Type      string                 `json:"type"`
	UserID    string                 `json:"user_id,omitempty"`
	ChannelID string                 `json:"channel_id,omitempty"`
	Timestamp string                 `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// StreamStats tracks streaming statistics
type StreamStats struct {
	StartTime        time.Time
	TotalBytes       int64
	TotalChunks      int64
	CurrentBitrate   float64
	Duration         time.Duration
	LastUpdateTime   time.Time
	BytesLastSecond  int64
	ChunksLastSecond int64
}

// OggOpusParser handles parsing Ogg containers to extract Opus packets
type OggOpusParser struct {
	buffer []byte
	state  int // 0=looking for sync, 1=reading page
}

func NewOggOpusParser() *OggOpusParser {
	return &OggOpusParser{
		buffer: make([]byte, 0, 8192),
		state:  0,
	}
}

// findOggSync finds the Ogg sync pattern in buffer
func (p *OggOpusParser) findOggSync() int {
	for i := 0; i <= len(p.buffer)-4; i++ {
		if p.buffer[i] == 'O' && p.buffer[i+1] == 'g' && p.buffer[i+2] == 'g' && p.buffer[i+3] == 'S' {
			return i
		}
	}
	return -1
}

// handleNoSync handles case when no sync pattern is found
func (p *OggOpusParser) handleNoSync() {
	// No sync found, keep last 3 bytes in case sync is split
	if len(p.buffer) > 3 {
		p.buffer = p.buffer[len(p.buffer)-3:]
	}
}

// calculatePageSizes calculates header and payload sizes for Ogg page
func (p *OggOpusParser) calculatePageSizes() (headerSize, payloadSize, totalPageSize int) {
	pageSegments := int(p.buffer[26])
	headerSize = 27 + pageSegments

	for i := 0; i < pageSegments; i++ {
		payloadSize += int(p.buffer[27+i])
	}

	totalPageSize = headerSize + payloadSize
	return
}

// isOpusHeaderPacket checks if payload is an Opus header packet
func isOpusHeaderPacket(payload []byte) bool {
	if len(payload) <= 8 {
		return false
	}
	headerType := string(payload[:8])
	return headerType == "OpusHead" || headerType == "OpusTags"
}

// extractOpusPackets extracts individual Opus packets from payload
func (p *OggOpusParser) extractOpusPackets(payload []byte, pageSegments int) [][]byte {
	var packets [][]byte
	segmentOffset := 0

	for i := 0; i < pageSegments; i++ {
		segmentSize := int(p.buffer[27+i])
		if segmentOffset+segmentSize <= len(payload) && segmentSize > 0 {
			// Extract individual Opus packet
			opusPacket := make([]byte, segmentSize)
			copy(opusPacket, payload[segmentOffset:segmentOffset+segmentSize])
			packets = append(packets, opusPacket)
			segmentOffset += segmentSize
		}
	}

	return packets
}

func (p *OggOpusParser) Parse(data []byte) ([][]byte, error) {
	p.buffer = append(p.buffer, data...)
	var packets [][]byte

	// Simple Ogg parser - look for Opus packets
	for len(p.buffer) > 27 { // Minimum Ogg page header size
		syncPos := p.findOggSync()

		if syncPos == -1 {
			p.handleNoSync()
			break
		}

		// Remove any data before sync
		if syncPos > 0 {
			p.buffer = p.buffer[syncPos:]
		}

		// Check if we have enough data for header
		if len(p.buffer) < 27 {
			break
		}

		headerSize, _, totalPageSize := p.calculatePageSizes()

		if len(p.buffer) < headerSize || len(p.buffer) < totalPageSize {
			break // Need more data
		}

		// Extract payload (skip header pages with OpusHead/OpusTags)
		payload := p.buffer[headerSize:totalPageSize]
		if !isOpusHeaderPacket(payload) {
			pageSegments := int(p.buffer[26])
			extractedPackets := p.extractOpusPackets(payload, pageSegments)
			packets = append(packets, extractedPackets...)
		}

		// Move to next page
		p.buffer = p.buffer[totalPageSize:]
	}

	return packets, nil
}

// StreamClient handles the WebSocket connection and audio streaming
type StreamClient struct {
	URL         string
	UserID      string
	Alias       string
	Channel     string
	PublishOnly bool
	ChunkSize   int
	conn        *websocket.Conn
	stats       *StreamStats
	ctx         context.Context
	cancel      context.CancelFunc
	retryCount  int
	maxRetries  int
	retryDelays []time.Duration
}

func main() {
	// Command line flags
	var (
		url       = flag.String("url", "ws://localhost:8080/ws", "WebSocket URL to connect to")
		userID    = flag.String("user", "", "User ID (generated if empty)")
		alias     = flag.String("alias", "", "User alias (generated if empty)")
		channel   = flag.String("channel", "general", "Channel to join")
		chunkSize = flag.Int("chunk-size", 1024, "Audio chunk size in bytes")
		help      = flag.Bool("help", false, "Show help message")
	)

	flag.Parse()

	if *help {
		showHelp()
		return
	}

	// Generate random user ID and alias if not provided
	if *userID == "" {
		*userID = generateRandomID()
	}
	if *alias == "" {
		*alias = fmt.Sprintf("stream-%s", (*userID)[:8])
	}

	// Create stream client
	client := &StreamClient{
		URL:         *url,
		UserID:      *userID,
		Alias:       *alias,
		Channel:     *channel,
		PublishOnly: true,
		ChunkSize:   *chunkSize,
		stats: &StreamStats{
			StartTime:      time.Now(),
			LastUpdateTime: time.Now(),
		},
		retryCount:  0,
		maxRetries:  -1, // Retry indefinitely
		retryDelays: []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second},
	}

	client.ctx, client.cancel = context.WithCancel(context.Background())

	// Handle interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n🛑 Shutting down...")
		client.cancel()
	}()

	// Connect with retry logic
	if err := client.ConnectWithRetry(); err != nil {
		log.Fatalf("Failed to establish initial connection after retries: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("Error closing client: %v", err)
		}
	}()

	fmt.Printf("🎙️ WhoTalkie Stream Publisher\n")
	fmt.Printf("   URL: %s\n", client.URL)
	fmt.Printf("   User: %s (%s)\n", client.Alias, client.UserID)
	fmt.Printf("   Channel: %s\n", client.Channel)
	fmt.Printf("   Mode: Publish-Only (Opus from stdin)\n\n")
	fmt.Printf("📡 Streaming... (Ctrl+C to stop)\n\n")

	// Start streaming from stdin with retry on errors
	if err := client.StreamFromStdinWithRetry(); err != nil && err != io.EOF {
		log.Printf("Streaming ended: %v", err)
	}

	fmt.Println("\n✅ Stream completed successfully")
}

func (c *StreamClient) Connect() error {
	var err error
	c.conn, _, err = websocket.Dial(c.ctx, c.URL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}

	// Join the channel
	joinEvent := PTTEvent{
		Type:      "channel_join",
		UserID:    c.UserID,
		ChannelID: c.Channel,
		Timestamp: time.Now().Format(time.RFC3339),
		Data: map[string]interface{}{
			"channel_name": c.Channel,
			"username":     c.Alias,
			"publish_only": c.PublishOnly,
		},
	}

	if err := wsjson.Write(c.ctx, c.conn, joinEvent); err != nil {
		return fmt.Errorf("failed to join channel: %w", err)
	}

	// Send PTT start event
	pttStartEvent := PTTEvent{
		Type:      "ptt_start",
		UserID:    c.UserID,
		ChannelID: c.Channel,
		Timestamp: time.Now().Format(time.RFC3339),
		Data: map[string]interface{}{
			"username":     c.Alias,
			"publish_only": true,
		},
	}

	if err := wsjson.Write(c.ctx, c.conn, pttStartEvent); err != nil {
		return fmt.Errorf("failed to send ptt_start: %w", err)
	}

	return nil
}

// shouldStopRetrying checks if we should stop retrying
func (c *StreamClient) shouldStopRetrying(err error) bool {
	return c.maxRetries >= 0 && c.retryCount >= c.maxRetries
}

// getRetryDelay calculates the delay for the current retry attempt
func (c *StreamClient) getRetryDelay() time.Duration {
	delayIndex := c.retryCount
	if delayIndex >= len(c.retryDelays) {
		delayIndex = len(c.retryDelays) - 1
	}
	return c.retryDelays[delayIndex]
}

// handleSuccessfulConnection handles successful reconnection
func (c *StreamClient) handleSuccessfulConnection() {
	if c.retryCount > 0 {
		fmt.Printf("\n✅ Reconnected successfully after %d attempts\n", c.retryCount)
		c.retryCount = 0 // Reset retry count on successful connection
	}
}

// handleFailedConnection handles failed connection attempts
func (c *StreamClient) handleFailedConnection(err error) {
	c.retryCount++
	fmt.Printf("\n❌ Connection failed (attempt %d): %v\n", c.retryCount, err)
	retryDelay := c.getRetryDelay()
	fmt.Printf("🔄 Retrying in %v...\n", retryDelay)
}

// waitForRetry waits for retry delay or context cancellation
func (c *StreamClient) waitForRetry() error {
	retryDelay := c.getRetryDelay()
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-time.After(retryDelay):
		return nil
	}
}

func (c *StreamClient) ConnectWithRetry() error {
	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
		}

		err := c.Connect()
		if err == nil {
			c.handleSuccessfulConnection()
			return nil
		}

		if c.shouldStopRetrying(err) {
			return fmt.Errorf("max retries (%d) exceeded: %w", c.maxRetries, err)
		}

		c.handleFailedConnection(err)

		if err := c.waitForRetry(); err != nil {
			return err
		}
	}
}

// processAudioData processes audio data and sends Opus packets
func (c *StreamClient) processAudioData(data []byte, parser *OggOpusParser) error {
	opusPackets, err := parser.Parse(data)
	if err != nil {
		log.Printf("Warning: Failed to parse Ogg data: %v", err)
		// Fallback: try sending raw data (might be pre-extracted Opus)
		return c.sendAudioChunk(data)
	}

	// Send each extracted Opus packet
	for _, packet := range opusPackets {
		if err := c.sendAudioChunk(packet); err != nil {
			return fmt.Errorf("failed to send opus packet: %w", err)
		}
	}

	return nil
}

// readAudioChunk reads a chunk of audio data from stdin
func (c *StreamClient) readAudioChunk(reader *bufio.Reader, buffer []byte) ([]byte, error) {
	n, err := reader.Read(buffer)
	if err != nil {
		if err == io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read from stdin: %w", err)
	}

	if n == 0 {
		return nil, nil
	}

	return buffer[:n], nil
}

// updateStreamStats updates streaming statistics
func (c *StreamClient) updateStreamStats(bytesRead int) {
	c.stats.TotalBytes += int64(bytesRead)
	c.stats.TotalChunks++
}

func (c *StreamClient) StreamFromStdin() error {
	// Start statistics display goroutine
	go c.updateStatsDisplay()

	// Create Ogg parser to extract Opus packets
	parser := NewOggOpusParser()

	reader := bufio.NewReader(os.Stdin)
	buffer := make([]byte, c.ChunkSize)

	for {
		select {
		case <-c.ctx.Done():
			return nil
		default:
			// Read chunk from stdin
			chunk, err := c.readAudioChunk(reader, buffer)
			if err != nil {
				return err
			}

			if chunk != nil {
				if err := c.processAudioData(chunk, parser); err != nil {
					return err
				}

				// Update statistics
				c.updateStreamStats(len(chunk))
			}
		}
	}
}

func (c *StreamClient) StreamFromStdinWithRetry() error {
	for {
		select {
		case <-c.ctx.Done():
			return nil
		default:
		}

		err := c.StreamFromStdin()
		if err == io.EOF {
			return err // EOF is not a retryable error
		}
		if err == nil {
			return nil // Normal completion
		}

		// Check if it's a connection-related error
		if c.isConnectionError(err) {
			fmt.Printf("\n⚠️ Connection lost during streaming: %v\n", err)

			// Close current connection
			if c.conn != nil {
				if err := c.conn.Close(websocket.StatusAbnormalClosure, "Connection lost"); err != nil {
					log.Printf("Error closing connection: %v", err)
				}
				c.conn = nil
			}

			// Attempt to reconnect
			if err := c.ConnectWithRetry(); err != nil {
				return fmt.Errorf("failed to reconnect: %w", err)
			}

			fmt.Printf("🔄 Resuming stream...\n")
			continue // Retry streaming
		}

		// Non-connection error, return it
		return err
	}
}

// hasConnectionKeyword checks if error contains connection-related keywords
func hasConnectionKeyword(errorStr string) bool {
	connectionKeywords := []string{
		"connection reset", "connection refused", "broken pipe",
		"websocket", "network", "timeout", "closed",
	}

	for _, keyword := range connectionKeywords {
		if strings.Contains(errorStr, keyword) {
			return true
		}
	}
	return false
}

// hasWriteKeyword checks if error contains write-related keywords
func hasWriteKeyword(errorStr string) bool {
	writeKeywords := []string{
		"failed to write", "failed to flush", "failed to marshal JSON",
	}

	for _, keyword := range writeKeywords {
		if strings.Contains(errorStr, keyword) {
			return true
		}
	}
	return false
}

// isNonStdinEOF checks if error is EOF but not from stdin
func isNonStdinEOF(errorStr string) bool {
	return strings.Contains(errorStr, "EOF") && !strings.Contains(errorStr, "stdin")
}

func (c *StreamClient) isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errorStr := err.Error()
	// Check for common connection-related error patterns
	return hasConnectionKeyword(errorStr) ||
		hasWriteKeyword(errorStr) ||
		isNonStdinEOF(errorStr)
}

func (c *StreamClient) sendAudioChunk(data []byte) error {
	// Send metadata first
	audioEvent := PTTEvent{
		Type:      "audio_data",
		UserID:    c.UserID,
		ChannelID: c.Channel,
		Timestamp: time.Now().Format(time.RFC3339),
		Data: map[string]interface{}{
			"chunk_size":  len(data),
			"format":      "opus",
			"sample_rate": 48000,
			"channels":    1,
		},
	}

	if err := wsjson.Write(c.ctx, c.conn, audioEvent); err != nil {
		return fmt.Errorf("failed to send audio metadata: %w", err)
	}

	// Send binary audio data
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, data); err != nil {
		return fmt.Errorf("failed to send audio data: %w", err)
	}

	return nil
}

func (c *StreamClient) updateStatsDisplay() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.displayStats()
		}
	}
}

func (c *StreamClient) displayStats() {
	now := time.Now()
	c.stats.Duration = now.Sub(c.stats.StartTime)

	// Calculate bitrate (bytes per second * 8 / 1000 = kbps)
	if c.stats.Duration.Seconds() > 0 {
		bytesPerSecond := float64(c.stats.TotalBytes) / c.stats.Duration.Seconds()
		c.stats.CurrentBitrate = (bytesPerSecond * 8) / 1000 // Convert to kbps
	}

	// Calculate instantaneous stats
	timeSinceLastUpdate := now.Sub(c.stats.LastUpdateTime)
	if timeSinceLastUpdate.Seconds() >= 1.0 {
		c.stats.BytesLastSecond = c.stats.TotalBytes - c.stats.BytesLastSecond
		c.stats.ChunksLastSecond = c.stats.TotalChunks - c.stats.ChunksLastSecond
		c.stats.LastUpdateTime = now
	}

	// Create stats line
	statsLine := fmt.Sprintf("📊 Duration: %s | Chunks: %d | Bytes: %s | Bitrate: %.1f kbps | Rate: %d chunks/s",
		formatDuration(c.stats.Duration),
		c.stats.TotalChunks,
		formatBytes(c.stats.TotalBytes),
		c.stats.CurrentBitrate,
		c.stats.ChunksLastSecond,
	)

	// Clear line and print stats (carriage return overwrites)
	fmt.Printf("\r%s", strings.Repeat(" ", 100)) // Clear line
	fmt.Printf("\r%s", statsLine)
}

func (c *StreamClient) Close() error {
	if c.conn != nil {
		// Send PTT end event
		pttEndEvent := PTTEvent{
			Type:      "ptt_end",
			UserID:    c.UserID,
			ChannelID: c.Channel,
			Timestamp: time.Now().Format(time.RFC3339),
			Data: map[string]interface{}{
				"username": c.Alias,
			},
		}
		if err := wsjson.Write(c.ctx, c.conn, pttEndEvent); err != nil {
			log.Printf("Error sending PTT end event: %v", err)
		}

		return c.conn.Close(websocket.StatusNormalClosure, "Stream ended")
	}
	return nil
}

func generateRandomID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	randBytes := make([]byte, 12)

	if _, err := rand.Read(randBytes); err != nil {
		log.Printf("Error generating random ID: %v", err)
		// Fallback to timestamp-based ID
		return fmt.Sprintf("fallback%d", time.Now().UnixNano()%1000000)
	}

	for i := range b {
		b[i] = charset[int(randBytes[i])%len(charset)]
	}
	return string(b)
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func showHelp() {
	fmt.Printf(`🎙️ WhoTalkie Stream Publisher

USAGE:
    whotalkie-stream [OPTIONS]

DESCRIPTION:
    Publishes Opus audio from stdin to a WhoTalkie channel. Parses Ogg Opus
    container format from ffmpeg and extracts raw Opus packets for transmission.

OPTIONS:
    -url string
        WebSocket URL to connect to (default: ws://localhost:8080/ws)
    -user string
        User ID (randomly generated if not specified)
    -alias string
        User alias/display name (auto-generated if not specified)
    -channel string
        Channel name to join (default: general)
    -chunk-size int
        Audio chunk size in bytes (default: 1024)
    -help
        Show this help message

EXAMPLES:
    # Stream file to WhoTalkie
    ffmpeg -i input.mp3 -c:a libopus -b:a 32k -f opus - | whotalkie-stream

    # Stream live radio/internet stream 
    ffmpeg -i https://stream.example.com/radio -c:a libopus -b:a 32k -f opus - | \
        whotalkie-stream -channel "music" -alias "Radio Stream"

    # Stream from microphone (live) 
    ffmpeg -f avfoundation -i ":0" -c:a libopus -b:a 32k -f opus - | \
        whotalkie-stream -channel "live" -alias "Live Mic"

    # Stream with custom settings
    ffmpeg -i input.wav -c:a libopus -b:a 32k -f opus - | \
        whotalkie-stream -channel "music" -alias "DJ Bot"

    # Connect to remote server
    ffmpeg -i audio.mp3 -c:a libopus -b:a 32k -f opus - | \
        whotalkie-stream -url "ws://example.com:8080/ws"

FEATURES:
    • Real-time streaming statistics
    • Automatic user ID and alias generation  
    • Publish-only mode (no audio reception)
    • Graceful shutdown with Ctrl+C
    • Compatible with any Opus audio source
    • Automatic reconnection with progressive backoff (10s, 30s, 1m)
    • Resilient streaming that continues through server outages

`)
}
