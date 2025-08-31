# WhoTalkie Client Library

A Go library for building WhoTalkie streaming clients with variable bitrate and stereo audio support.

## Overview

This library provides a clean, event-driven interface for connecting to WhoTalkie servers and streaming audio with advanced capabilities like variable bitrate, stereo audio, and automatic capability negotiation.

## Features

- **Variable Bitrate**: Support for 32kbps, 64kbps, and 128kbps audio
- **Stereo Audio**: Full 2-channel stereo transmission and reception
- **Capability Negotiation**: Automatic format negotiation with server
- **Event-Driven**: Comprehensive callback system for all server events
- **Type Safety**: Fully typed Go interfaces and structures
- **Context Support**: Full context.Context integration for cancellation
- **Streaming Utilities**: High-level streaming client with convenience methods

## Installation

```bash
go get whotalkie/pkg/client
```

## Quick Start

### Basic Client

```go
package main

import (
    "context"
    "log"
    "whotalkie/pkg/client"
)

func main() {
    config := client.ClientConfig{
        ServerURL:   "ws://localhost:8080/ws",
        Username:    "MyBot",
        Channel:     "general",
        Bitrate:     64000,
        Channels:    2, // Stereo
        PublishOnly: true,
    }

    client := client.NewStreamClient(config)
    ctx := context.Background()

    // Connect and negotiate capabilities
    if err := client.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer client.Disconnect()

    if err := client.NegotiateCapabilities(ctx); err != nil {
        log.Fatal(err)
    }

    if err := client.JoinChannel(ctx); err != nil {
        log.Fatal(err)
    }

    // Stream audio
    if err := client.StartTransmission(ctx); err != nil {
        log.Fatal(err)
    }

    // Send audio data
    audioData := make([]byte, 1024) // Your audio data
    if err := client.SendAudioData(ctx, audioData); err != nil {
        log.Fatal(err)
    }

    if err := client.StopTransmission(ctx); err != nil {
        log.Fatal(err)
    }
}
```

### Streaming Client

```go
package main

import (
    "context"
    "time"
    "whotalkie/pkg/client"
)

func main() {
    config := client.ClientConfig{
        ServerURL: "ws://localhost:8080/ws",
        Username:  "StreamBot",
        Channel:   "music",
        Bitrate:   128000, // High quality
        Channels:  2,      // Stereo
    }

    streamClient := client.NewStreamingClient(config)
    ctx := context.Background()

    // All-in-one connect and setup
    if err := streamClient.ConnectAndSetup(ctx); err != nil {
        log.Fatal(err)
    }
    defer streamClient.Disconnect()

    // Listen for server messages
    go streamClient.ListenForMessages(ctx)

    // Stream for 10 minutes with 1 second intervals
    duration := 10 * time.Minute
    interval := 1 * time.Second
    chunkSize := 1024

    if err := streamClient.StreamForDuration(ctx, duration, interval, chunkSize); err != nil {
        log.Printf("Stream error: %v", err)
    }
}
```

### Streaming from Input Source

```go
package main

import (
    "context"
    "os"
    "whotalkie/pkg/client"
)

func main() {
    config := client.ClientConfig{
        ServerURL: "ws://localhost:8080/ws",
        Username:  "StreamBot",
        Bitrate:   64000,  // 64kbps
        Channels:  2,      // Stereo
    }

    streamClient := client.NewStreamingClient(config)
    ctx := context.Background()

    if err := streamClient.ConnectAndSetup(ctx); err != nil {
        log.Fatal(err)
    }
    defer streamClient.Disconnect()

    // Stream from stdin (for ffmpeg piping)
    // Example: ffmpeg -i input.mp3 -f s16le -ar 48000 -ac 2 - | go run main.go
    if err := streamClient.StreamFromReader(ctx, os.Stdin, 1024); err != nil {
        log.Printf("Stream error: %v", err)
    }
}
```

## Event Handling

### Custom Event Handler

```go
type MyEventHandler struct{}

func (h *MyEventHandler) OnConnected() {
    fmt.Println("🎉 Connected!")
}

func (h *MyEventHandler) OnCapabilityNegotiated(accepted bool) {
    if accepted {
        fmt.Println("✅ Server accepted our capabilities")
    } else {
        fmt.Println("❌ Server rejected our capabilities")
    }
}

func (h *MyEventHandler) OnPTTStart(username string) {
    fmt.Printf("🎙️ %s started talking\n", username)
}

func (h *MyEventHandler) OnError(message, code string) {
    fmt.Printf("❌ Error [%s]: %s\n", code, message)
}

// Implement other EventHandler methods...

// Use the custom handler
client := client.NewStreamClient(config)
client.SetEventHandler(&MyEventHandler{})
```

### Default Event Handler

```go
// Uses client.DefaultEventHandler which logs to standard logger
client := client.NewStreamClient(config)
// DefaultEventHandler is used automatically
```

## Configuration

### ClientConfig

```go
type ClientConfig struct {
    ServerURL   string // WebSocket server URL
    Username    string // Display username
    Channel     string // Channel to join
    Bitrate     int    // Audio bitrate (32000, 64000, 128000)
    Channels    int    // 1 for mono, 2 for stereo
    PublishOnly bool   // Publish-only mode
    UserAgent   string // Custom user agent (optional)
}
```

### Audio Capabilities

```go
// High-quality streaming client capabilities
capabilities := client.CreateHighQualityCapabilities(
    "my-client",           // Client type
    "MyApp/1.0.0",        // User agent
    128000,               // Bitrate
    2,                    // Channels (stereo)
)

// Web client capabilities (restricted)
capabilities := client.DefaultWebClientCapabilities()
```

## Advanced Usage

### Manual Capability Negotiation

```go
client := client.NewStreamClient(config)

// Connect first
if err := client.Connect(ctx); err != nil {
    log.Fatal(err)
}

// Get and modify capabilities
caps := client.GetCapabilities()
caps.TransmitFormat.Bitrate = 128000

// Send negotiation
event := client.PTTEvent{
    Type: "capability_negotiation",
    Timestamp: time.Now(),
    Data: map[string]interface{}{
        "capabilities": caps,
    },
}

// Send manually (or use NegotiateCapabilities)
```

### Custom Audio Processing

```go
client := client.NewStreamClient(config)

// Start transmission
client.StartTransmission(ctx)

// Process and send audio in chunks
for audioChunk := range audioSource {
    // Your audio processing here
    processedAudio := processAudio(audioChunk)
    
    if err := client.SendAudioData(ctx, processedAudio); err != nil {
        log.Printf("Send error: %v", err)
        break
    }
}

client.StopTransmission(ctx)
```

### Multiple Clients

```go
// Create multiple clients for different channels
clients := make([]*client.StreamingClient, 0)

for i, channel := range channels {
    config := client.ClientConfig{
        ServerURL: serverURL,
        Username:  fmt.Sprintf("Bot%d", i),
        Channel:   channel,
        Bitrate:   64000,
        Channels:  1,
    }
    
    streamClient := client.NewStreamingClient(config)
    if err := streamClient.ConnectAndSetup(ctx); err != nil {
        continue
    }
    
    clients = append(clients, streamClient)
    
    // Start streaming in background
    go streamClient.StreamForDuration(ctx, time.Hour, time.Second, 1024)
}

// Cleanup all clients
defer func() {
    for _, c := range clients {
        c.Disconnect()
    }
}()
```

## Types Reference

### Core Types

```go
type AudioFormat struct {
    Codec      string `json:"codec"`       // "opus"
    Bitrate    int    `json:"bitrate"`     // 32000, 64000, 128000
    SampleRate int    `json:"sample_rate"` // 48000
    Channels   int    `json:"channels"`    // 1 or 2
}

type ClientCapabilities struct {
    ClientType          string        `json:"client_type"`
    UserAgent          string        `json:"user_agent"`
    SupportedFormats   []AudioFormat `json:"supported_formats"`
    TransmitFormat     AudioFormat   `json:"transmit_format"`
    SupportsVariableBR bool          `json:"supports_variable_br"`
    SupportsStereo     bool          `json:"supports_stereo"`
}

type PTTEvent struct {
    Type      string                 `json:"type"`
    UserID    string                 `json:"user_id,omitempty"`
    ChannelID string                 `json:"channel_id,omitempty"`
    Timestamp time.Time              `json:"timestamp"`
    Data      map[string]interface{} `json:"data,omitempty"`
}
```

### Event Handler Interface

```go
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
```

## Client Methods

### StreamClient

```go
// Connection management
Connect(ctx context.Context) error
Disconnect() error
IsConnected() bool

// Protocol methods
NegotiateCapabilities(ctx context.Context) error
JoinChannel(ctx context.Context) error
StartTransmission(ctx context.Context) error
StopTransmission(ctx context.Context) error
SendAudioData(ctx context.Context, audioData []byte) error

// Message handling
ListenForMessages(ctx context.Context) error

// Configuration
SetEventHandler(handler EventHandler)
GetUserID() string
GetCapabilities() ClientCapabilities
```

### StreamingClient

```go
// Extends StreamClient with convenience methods

// All-in-one setup
ConnectAndSetup(ctx context.Context) error

// Streaming state
IsStreaming() bool
StartStreaming(ctx context.Context) error
StopStreaming(ctx context.Context) error

// Utility methods
SendTestAudio(ctx context.Context, size int) error
StreamForDuration(ctx context.Context, duration, interval time.Duration, chunkSize int) error
```

## Error Handling

```go
// Connection errors
if err := client.Connect(ctx); err != nil {
    if errors.Is(err, context.Canceled) {
        // Context was canceled
    } else if strings.Contains(err.Error(), "connection refused") {
        // Server not available
    } else {
        // Other connection error
    }
}

// Streaming errors are reported via EventHandler.OnError
type MyHandler struct{}

func (h *MyHandler) OnError(message, code string) {
    switch code {
    case "INVALID_FORMAT":
        // Audio format not supported
    case "NO_CHANNEL":
        // Not in a channel
    case "CONNECTION_LOST":
        // Network issue
    default:
        log.Printf("Unknown error: %s", message)
    }
}
```

## Best Practices

1. **Always use contexts**: Pass context.Context for cancellation support
2. **Handle events**: Implement EventHandler for robust applications
3. **Graceful cleanup**: Always call Disconnect() in defer statements
4. **Error handling**: Check all method return values
5. **Resource management**: Use StreamingClient for complex applications
6. **Testing**: Use test audio generation for development

## Examples

See `cmd/whotalkie-stream` for a complete CLI implementation using this library.

## Thread Safety

This library is **not** thread-safe. Use appropriate synchronization if accessing from multiple goroutines. The recommended pattern is to use one client per goroutine or protect access with mutexes.