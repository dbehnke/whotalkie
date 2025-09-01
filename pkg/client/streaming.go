package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"

	"whotalkie/internal/oggdemux"
)

// StreamingClient extends StreamClient with convenient streaming utilities
type StreamingClient struct {
	*StreamClient
	isStreaming bool
}

// NewStreamingClient creates a new streaming client with convenient methods
func NewStreamingClient(config ClientConfig) *StreamingClient {
	return &StreamingClient{
		StreamClient: NewStreamClient(config),
		isStreaming:  false,
	}
}

// IsStreaming returns whether the client is currently streaming
func (c *StreamingClient) IsStreaming() bool {
	return c.isStreaming
}

// ConnectAndSetup performs the full connection and setup process
func (c *StreamingClient) ConnectAndSetup(ctx context.Context) error {
	// Connect to server
	if err := c.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Negotiate capabilities
	if err := c.NegotiateCapabilities(ctx); err != nil {
		return fmt.Errorf("failed to negotiate capabilities: %w", err)
	}

	// Small delay to allow server to process negotiation
	time.Sleep(500 * time.Millisecond)

	// Join channel
	if err := c.JoinChannel(ctx); err != nil {
		return fmt.Errorf("failed to join channel: %w", err)
	}

	// Small delay to allow channel join to complete
	time.Sleep(1 * time.Second)

	return nil
}

// StartStreaming begins streaming with PTT
func (c *StreamingClient) StartStreaming(ctx context.Context) error {
	if c.isStreaming {
		return fmt.Errorf("already streaming")
	}

	if err := c.StartTransmission(ctx); err != nil {
		return fmt.Errorf("failed to start streaming: %w", err)
	}

	c.isStreaming = true
	return nil
}

// StopStreaming stops streaming
func (c *StreamingClient) StopStreaming(ctx context.Context) error {
	if !c.isStreaming {
		return fmt.Errorf("not currently streaming")
	}

	if err := c.StopTransmission(ctx); err != nil {
		return fmt.Errorf("failed to stop streaming: %w", err)
	}

	c.isStreaming = false
	return nil
}

// SendTestAudio sends dummy audio data for testing
func (c *StreamingClient) SendTestAudio(ctx context.Context, size int) error {
	if !c.isStreaming {
		return fmt.Errorf("not currently streaming")
	}

	// Generate silence (zeros)
	audioData := make([]byte, size)
	
	return c.SendAudioData(ctx, audioData)
}

// SendOpusData sends Opus audio data as passthrough
func (c *StreamingClient) SendOpusData(ctx context.Context, opusData []byte) error {
	if !c.isStreaming {
		return fmt.Errorf("not currently streaming")
	}

	// Pass through Opus data from FFmpeg to server for relay to web clients
	return c.SendAudioData(ctx, opusData)
}

// StreamForDuration streams test audio for a specified duration
func (c *StreamingClient) StreamForDuration(ctx context.Context, duration time.Duration, interval time.Duration, chunkSize int) error {
	if err := c.StartStreaming(ctx); err != nil {
		return err
	}
	defer func() { _ = c.StopStreaming(ctx) }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	durationTimer := time.NewTimer(duration)
	defer durationTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-durationTimer.C:
			return nil // Duration completed
		case <-ticker.C:
			if err := c.SendTestAudio(ctx, chunkSize); err != nil {
				return fmt.Errorf("error sending test audio: %w", err)
			}
		}
	}
}

// StreamInfinite streams test audio indefinitely until context is cancelled
func (c *StreamingClient) StreamInfinite(ctx context.Context, interval time.Duration, chunkSize int) error {
	if err := c.StartStreaming(ctx); err != nil {
		return err
	}
	defer func() { _ = c.StopStreaming(ctx) }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.SendTestAudio(ctx, chunkSize); err != nil {
				return fmt.Errorf("error sending test audio: %w", err)
			}
		}
	}
}

// StreamFromReader streams audio data from an io.Reader (e.g., stdin)
func (c *StreamingClient) StreamFromReader(ctx context.Context, reader io.Reader, chunkSize int) error {
	if err := c.StartStreaming(ctx); err != nil {
		return err
	}
	defer func() { _ = c.StopStreaming(ctx) }()

	bufReader := bufio.NewReader(reader)
	buffer := make([]byte, chunkSize)

	pr, pw := io.Pipe()
	c.startDemuxerForMeta(ctx, pr)

	return c.streamLoop(ctx, bufReader, buffer, pw)
}

func (c *StreamingClient) startDemuxerForMeta(ctx context.Context, pr *io.PipeReader) {
	demux := oggdemux.New(pr)
	
	go func() {
		var lastTitle string
		for {
			page, err := demux.NextPage()
			if err != nil {
				return
			}
			if page == nil {
				continue
			}
			c.processPageForMeta(ctx, page, &lastTitle)
		}
	}()
}

func (c *StreamingClient) processPageForMeta(ctx context.Context, page *oggdemux.Page, lastTitle *string) {
	for _, pkt := range page.Packets {
		if pkt.IsVorbisComment {
			c.handleVorbisComment(ctx, pkt.Data, lastTitle)
		}
	}
}

func (c *StreamingClient) handleVorbisComment(ctx context.Context, data []byte, lastTitle *string) {
	title := parseOpusTagsTitle(data)
	sendVal := c.getSendValue(title, data)
	
	if sendVal != *lastTitle {
		*lastTitle = sendVal
		_ = c.SendMeta(ctx, sendVal)
	}
}

func (c *StreamingClient) getSendValue(title string, data []byte) string {
	if title != "" {
		return title
	}
	return string(data)
}

func (c *StreamingClient) streamLoop(ctx context.Context, bufReader *bufio.Reader, buffer []byte, pw *io.PipeWriter) error {
	defer pw.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := c.processNextChunk(ctx, bufReader, buffer, pw); err != nil {
				return err
			}
		}
	}
}

func (c *StreamingClient) processNextChunk(ctx context.Context, bufReader *bufio.Reader, buffer []byte, pw *io.PipeWriter) error {
	n, err := bufReader.Read(buffer)
	if err != nil {
		return c.handleReadError(err)
	}

	if n > 0 {
		return c.sendChunkData(ctx, buffer[:n], pw)
	}
	return nil
}

func (c *StreamingClient) handleReadError(err error) error {
	if err == io.EOF {
		return nil
	}
	return fmt.Errorf("error reading from stream: %w", err)
}

func (c *StreamingClient) sendChunkData(ctx context.Context, data []byte, pw *io.PipeWriter) error {
	_, _ = pw.Write(data)
	
	if err := c.SendOpusData(ctx, data); err != nil {
		return fmt.Errorf("error sending chunk to server: %w", err)
	}
	return nil
}

// parseOpusTagsTitle parses an OpusTags packet (Vorbis comments) and
// returns the first TITLE comment value it finds (case-insensitive), or
// empty string if none present.
func parseOpusTagsTitle(data []byte) string {
	if !isValidOpusTagsData(data) {
		return ""
	}
	
	r := bytes.NewReader(data[8:])
	if !skipVendorString(r) {
		return ""
	}
	
	listLen, ok := readListLength(r)
	if !ok {
		return ""
	}
	
	return findTitleInComments(r, listLen)
}

func isValidOpusTagsData(data []byte) bool {
	if len(data) < 16 {
		return false
	}
	return string(data[0:8]) == "OpusTags"
}

func skipVendorString(r *bytes.Reader) bool {
	var vendorLen uint32
	if err := binary.Read(r, binary.LittleEndian, &vendorLen); err != nil {
		return false
	}
	
	if int(vendorLen) > r.Len() {
		return false
	}
	
	_, err := r.Seek(int64(vendorLen), io.SeekCurrent)
	return err == nil
}

func readListLength(r *bytes.Reader) (uint32, bool) {
	var listLen uint32
	err := binary.Read(r, binary.LittleEndian, &listLen)
	return listLen, err == nil
}

func findTitleInComments(r *bytes.Reader, listLen uint32) string {
	for i := uint32(0); i < listLen; i++ {
		comment, ok := readNextComment(r)
		if !ok {
			return ""
		}
		
		if title := extractTitleFromComment(comment); title != "" {
			return title
		}
	}
	return ""
}

func readNextComment(r *bytes.Reader) (string, bool) {
	var clen uint32
	if err := binary.Read(r, binary.LittleEndian, &clen); err != nil {
		return "", false
	}
	
	if int(clen) > r.Len() {
		return "", false
	}
	
	buf := make([]byte, clen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", false
	}
	
	return string(buf), true
}

func extractTitleFromComment(comment string) string {
	parts := strings.SplitN(comment, "=", 2)
	if len(parts) != 2 {
		return ""
	}
	
	if strings.EqualFold(strings.TrimSpace(parts[0]), "TITLE") {
		return strings.TrimSpace(parts[1])
	}
	
	return ""
}