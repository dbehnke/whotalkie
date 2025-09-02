package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"whotalkie/pkg/client"
	"whotalkie/internal/otelutil"
)

// CLIEventHandler provides enhanced logging for the CLI application
type CLIEventHandler struct {
	verbose bool
}

func NewCLIEventHandler(verbose bool) *CLIEventHandler {
	return &CLIEventHandler{verbose: verbose}
}

func (h *CLIEventHandler) OnConnected() {
	log.Printf("✅ Connected to WhoTalkie server")
}

func (h *CLIEventHandler) OnDisconnected() {
	log.Printf("❌ Disconnected from server")
}

func (h *CLIEventHandler) OnCapabilityNegotiated(accepted bool) {
	if accepted {
		log.Printf("✅ Server accepted our capabilities")
	} else {
		log.Printf("❌ Server rejected our capabilities")
	}
}

func (h *CLIEventHandler) OnChannelJoined(channel string) {
	log.Printf("📡 Joined channel: %s", channel)
}

func (h *CLIEventHandler) OnPTTStart(username string) {
	log.Printf("🎙️ %s started talking", username)
}

func (h *CLIEventHandler) OnPTTEnd(username string) {
	log.Printf("🔇 %s stopped talking", username)
}

func (h *CLIEventHandler) OnUserJoin(username string) {
	if h.verbose {
		log.Printf("👋 %s joined", username)
	}
}

func (h *CLIEventHandler) OnUserLeave(username string) {
	if h.verbose {
		log.Printf("👋 %s left", username)
	}
}

func (h *CLIEventHandler) OnError(message, code string) {
	log.Printf("❌ Server error [%s]: %s", code, message)
}

func (h *CLIEventHandler) OnServerEvent(eventType string, data map[string]interface{}) {
	if h.verbose {
		log.Printf("📥 Event: %s %v", eventType, data)
	}
}

func main() {
	// Initialize optional tracing (no-op unless WT_OTEL_STDOUT=1)
	_ = otelutil.Init()
	defer otelutil.Flush()

	cfg := parseFlags()
	runStreaming(cfg)
}

// parseFlags handles flag parsing and validation, returning a prepared config.
type runConfig struct {
	Client client.ClientConfig
	Duration time.Duration
	Interval time.Duration
	ChunkSize int
	Stdin bool
	Verbose bool
	Meta string
}

func parseFlags() runConfig {
	var (
	meta        = flag.String("meta", "", "Optional stream metadata/comments to send (e.g., title)")
		serverURL   = flag.String("server", "ws://localhost:8080/ws", "WhoTalkie server WebSocket URL")
		username    = flag.String("username", "", "Username for the stream client (required)")
		channel     = flag.String("channel", "general", "Channel to join")
		bitrate     = flag.Int("bitrate", 64000, "Audio bitrate in bps (32000, 64000, 128000)")
		stereo      = flag.Bool("stereo", true, "Use stereo audio (2 channels)")
		publishOnly = flag.Bool("publish-only", true, "Connect as publish-only client")
		duration    = flag.Int("duration", 0, "Test transmission duration in seconds (0 = infinite)")
		interval    = flag.Int("interval", 1000, "Audio chunk interval in milliseconds")
		chunkSize   = flag.Int("chunk-size", 1024, "Audio chunk size in bytes")
		verbose     = flag.Bool("verbose", false, "Enable verbose logging")
		stdin       = flag.Bool("stdin", false, "Stream audio from stdin (for ffmpeg piping)")
	)
	flag.Parse()

	if *username == "" {
		log.Fatal("❌ Username is required (-username)")
	}

	channels := 1
	if *stereo {
		channels = 2
	}

	// Validate bitrate
	validBitrates := []int{32000, 64000, 128000}
	bitrateValid := false
	for _, br := range validBitrates {
		if *bitrate == br {
			bitrateValid = true
			break
		}
	}
	if !bitrateValid {
		log.Fatalf("❌ Invalid bitrate %d. Valid options: %v", *bitrate, validBitrates)
	}

	// Display configuration
	log.Printf("🎵 WhoTalkie Stream Client")
	log.Printf("   Server: %s", *serverURL)
	log.Printf("   Username: %s", *username)
	log.Printf("   Channel: %s", *channel)
	log.Printf("   Audio: %dkbps, %s", *bitrate/1000, map[bool]string{true: "stereo", false: "mono"}[*stereo])
	log.Printf("   Publish-only: %v", *publishOnly)
	log.Printf("   Duration: %ds", *duration)
	if *meta != "" {
		log.Printf("   Meta: %s", *meta)
	}

	return runConfig{
		Client: client.ClientConfig{
			ServerURL:   *serverURL,
			Username:    *username,
			Channel:     *channel,
			Bitrate:     *bitrate,
			Channels:    channels,
			PublishOnly: *publishOnly,
			UserAgent:   "whotalkie-stream-cli/1.0.0",
		},
		Duration: time.Duration(*duration) * time.Second,
		Interval: time.Duration(*interval) * time.Millisecond,
		ChunkSize: *chunkSize,
		Stdin: *stdin,
		Verbose: *verbose,
	Meta: *meta,
	}
}

// runStreaming performs the connection and streaming lifetime using the
// prepared client config.
func runStreaming(cfg runConfig) {
	streamClient := client.NewStreamingClient(cfg.Client)
	streamClient.SetEventHandler(NewCLIEventHandler(cfg.Verbose))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	setupShutdownHandler(cancel)
	
	if err := streamClient.ConnectAndSetup(ctx); err != nil {
		log.Fatalf("❌ Failed to connect and setup: %v", err)
	}
	defer func() { _ = streamClient.Disconnect() }()

	startMessageListener(ctx, streamClient)
	startMetaSender(ctx, cfg.Meta, streamClient)
	
	runStreamingMode(ctx, cfg, streamClient)
	
	time.Sleep(500 * time.Millisecond)
}

func setupShutdownHandler(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("🛑 Shutting down...")
		cancel()
	}()
}

func startMessageListener(ctx context.Context, streamClient *client.StreamingClient) {
	go func() {
		if err := streamClient.ListenForMessages(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("❌ Message listening error: %v", err)
			}
		}
	}()
}

func startMetaSender(ctx context.Context, meta string, streamClient *client.StreamingClient) {
	if meta == "" {
		return
	}
	
	go func() {
		_ = streamClient.SendMeta(ctx, meta)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = streamClient.SendMeta(ctx, meta)
			}
		}
	}()
}

func runStreamingMode(ctx context.Context, cfg runConfig, streamClient *client.StreamingClient) {
	if cfg.Stdin {
		runStdinStreaming(ctx, cfg, streamClient)
	} else if cfg.Duration == 0 {
		runInfiniteStreaming(ctx, cfg, streamClient)
	} else {
		runDurationStreaming(ctx, cfg, streamClient)
	}
}

func runStdinStreaming(ctx context.Context, cfg runConfig, streamClient *client.StreamingClient) {
	log.Printf("🎤 Streaming from stdin (pipe ffmpeg output here)...")
	log.Printf("💡 Example: ffmpeg -i https://stream.zeno.fm/vgchxkqc998uv -f ogg -c:a libopus -b:a %dk -ac %d - | %s", cfg.Client.Bitrate/1000, cfg.Client.Channels, os.Args[0])
	handleStreamError(streamClient.StreamFromReader(ctx, os.Stdin, cfg.ChunkSize), ctx)
}

func runInfiniteStreaming(ctx context.Context, cfg runConfig, streamClient *client.StreamingClient) {
	log.Printf("🎤 Starting infinite test stream (Ctrl+C to stop)...")
	handleStreamError(streamClient.StreamInfinite(ctx, cfg.Interval, cfg.ChunkSize), ctx)
}

func runDurationStreaming(ctx context.Context, cfg runConfig, streamClient *client.StreamingClient) {
	log.Printf("🎤 Starting test stream for %s...", cfg.Duration)
	handleStreamError(streamClient.StreamForDuration(ctx, cfg.Duration, cfg.Interval, cfg.ChunkSize), ctx)
}

func handleStreamError(err error, ctx context.Context) {
	if err != nil {
		if ctx.Err() == context.Canceled {
			log.Printf("⏹️ Stream cancelled by user")
		} else {
			log.Printf("❌ Stream error: %v", err)
		}
	} else {
		log.Printf("✅ Stream completed successfully")
	}
}