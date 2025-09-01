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

	// Stream raw bytes (e.g., OGG container) directly to the server so the
	// server can perform demuxing. This matches the server's publish-only
	// pipeline which expects OGG bytes when publish-only is used with stdin.
	bufReader := bufio.NewReader(reader)
	buffer := make([]byte, chunkSize)

	// Create a pipe so we can run a local demuxer concurrently to detect
	// Vorbis comments (OpusTags) and send meta events from the streamer.
	// We write every chunk into the pipeWriter and the demuxer reads from
	// the pipeReader via oggdemux.New(pipeReader).
	pr, pw := io.Pipe()
	demux := oggdemux.New(pr)

	// demux goroutine: emits SendMeta when the TITLE (Vorbis comment) changes
	go func() {
		var lastTitle string
		for {
			page, err := demux.NextPage()
			if err != nil {
				// EOF or other errors end demux loop
				return
			}
			if page == nil {
				continue
			}
			for _, pkt := range page.Packets {
				if pkt.IsVorbisComment {
					title := parseOpusTagsTitle(pkt.Data)
					var sendVal string
					if title != "" {
						sendVal = title
					} else {
						// fallback to raw comments if no TITLE found
						sendVal = string(pkt.Data)
					}
					if sendVal != lastTitle {
						lastTitle = sendVal
						_ = c.SendMeta(ctx, sendVal)
					}
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			n, err := bufReader.Read(buffer)
			if err != nil {
				if err == io.EOF {
					return nil // End of stream
				}
				return fmt.Errorf("error reading from stream: %w", err)
			}

			if n > 0 {
				// Write into demux pipe (best-effort). If the demux goroutine
				// has exited, this may return an error — ignore it and continue
				// sending to the server.
				_, _ = pw.Write(buffer[:n])

				// Send raw chunk bytes (container data) to server. Server will
				// write these into its demux pipe and parse OGG pages itself.
				if err := c.SendOpusData(ctx, buffer[:n]); err != nil {
					return fmt.Errorf("error sending chunk to server: %w", err)
				}
			}
		}
	}
}

// parseOpusTagsTitle parses an OpusTags packet (Vorbis comments) and
// returns the first TITLE comment value it finds (case-insensitive), or
// empty string if none present.
func parseOpusTagsTitle(data []byte) string {
	// Need at least 8 bytes for "OpusTags" + 4 bytes vendor len + 4 bytes list len
	if len(data) < 16 {
		return ""
	}
	if string(data[0:8]) != "OpusTags" {
		return ""
	}
	r := bytes.NewReader(data[8:])
	var vendorLen uint32
	if err := binary.Read(r, binary.LittleEndian, &vendorLen); err != nil {
		return ""
	}
	// skip vendor string
	if int(vendorLen) > r.Len() {
		return ""
	}
	if _, err := r.Seek(int64(vendorLen), io.SeekCurrent); err != nil {
		return ""
	}
	var listLen uint32
	if err := binary.Read(r, binary.LittleEndian, &listLen); err != nil {
		return ""
	}
	for i := uint32(0); i < listLen; i++ {
		var clen uint32
		if err := binary.Read(r, binary.LittleEndian, &clen); err != nil {
			return ""
		}
		if int(clen) > r.Len() {
			return ""
		}
		buf := make([]byte, clen)
		if _, err := io.ReadFull(r, buf); err != nil {
			return ""
		}
		// comment is KEY=VALUE
		s := string(buf)
		parts := strings.SplitN(s, "=", 2)
		if len(parts) == 2 {
			if strings.EqualFold(strings.TrimSpace(parts[0]), "TITLE") {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}