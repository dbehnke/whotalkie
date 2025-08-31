# WhoTalkie - Real-Time Push-to-Talk Communication 🎙️

![Status: Production-Ready](https://img.shields.io/badge/Status-Production%20Ready-brightgreen)
![Audio: Opus WebCodecs](https://img.shields.io/badge/Audio-Opus%20WebCodecs-blue)
![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go)

A high-performance, real-time Push-to-Talk (PTT) application built with Go and modern web technologies. Think Discord meets walkie-talkie with professional-grade audio compression.

## ✨ Features

### 🎵 **Advanced Audio Features**
- **Variable Bitrate** - 32kbps, 64kbps, 128kbps support for different quality needs
- **Stereo Audio** - Full 2-channel stereo for high-quality streaming
- **WebCodecs API Integration** - Browser-native Opus encoding/decoding
- **Client-Specific Restrictions** - Web clients limited to 32kbps mono, streaming clients get full quality
- **Automatic Negotiation** - Capability-based format negotiation between client and server

### 📊 **Real-Time Statistics**
- **Live Audio Metrics** - Bitrate monitoring, byte counters, transmission duration
- **Performance Tracking** - Track bandwidth usage and connection quality
- **Smart UI Updates** - 100ms refresh rate with fade-in/out effects

### 🌐 **Modern Web Client**
- **Zero Installation** - Works directly in Chrome 94+, Firefox 106+
- **Intuitive Interface** - One-click PTT with visual feedback
- **Multi-User Mixing** - Simultaneous audio streams with automatic volume balancing
- **Channel Management** - Easy channel joining and user presence indicators

### ⚡ **High-Performance Server**
- **Go-Powered Backend** - Concurrent WebSocket handling with minimal latency
- **Pass-Through Architecture** - Server relays Opus packets without processing
- **Scalable Design** - Support for multiple channels and users
- **Real-Time Updates** - Live user presence and channel activity

## 📡 Channel Behavior & Broadcast Mode

WhoTalkie supports two distinct channel operating modes:

### 🎙️ **Normal PTT Mode**
- All users can transmit and receive audio
- Standard push-to-talk functionality
- Multiple users can take turns speaking

### 📻 **Broadcast Mode** (Publish-Only Channels)
- **Streaming bots/sources** join as "publish-only" clients
- **Normal users** become **listen-only** when publish-only clients are present
- **Design Intent**: Enables monitoring/broadcast scenarios

**Use Cases for Broadcast Mode:**
- **Live Radio Streaming**: Radio stations broadcasting to listeners
- **Security Monitoring**: Audio feeds from surveillance systems
- **Conference Recording**: Playback of recorded meetings
- **Alert Systems**: Emergency broadcasts or notifications
- **External Audio Monitoring**: Any one-way audio distribution

**Technical Behavior:**
```bash
# Publish-only streaming bot joins channel
./whotalkie-stream -channel "radio" -alias "Live Radio"

# Normal users joining this channel can only listen
# Their PTT buttons become disabled while the bot is streaming
```
 
## Observability helpers

This repository provides a small internal helper package `internal/cid` to
centralize correlation id (CID) handling. Use `cid.WithCID(ctx, cid)` to
attach a correlation id to a context and `cid.AddHeaderFromContext(headers, ctx)`
 to add the header named by `cidpkg.HeaderName` to outgoing HTTP/WebSocket dial requests so the
server and downstream services can correlate traces and logs.

Example:

```go
headers := map[string][]string{"User-Agent": {"my-client/1.0"}}
cid.AddHeaderFromContext(headers, ctx)
// pass headers into websocket.Dial or http.Request
```

**Note for Developers:** This behavior is intentional. Normal users are blocked from PTT when publish-only clients are present to maintain the broadcast/monitoring paradigm where the streaming source "owns" the channel.

## 🚀 Quick Start

### Prerequisites
- **Go 1.21+** for server
- **Modern Browser** with WebCodecs support:
  - Chrome 94+ ✅
  - Firefox 106+ ✅
  - Safari ❌ (WebCodecs not supported)

### Running the Server

```bash
# Clone the repository
git clone https://github.com/yourusername/whotalkie.git
cd whotalkie

# Run the server
go run cmd/server/main.go
```

### Using the Web Client

1. **Open your browser** to `http://localhost:8080`
2. **Grant microphone permission** when prompted
3. **Join a channel** (e.g., "general", "team1")
4. **Hold the PTT button** to transmit
5. **Watch real-time stats** during conversations

### Using the CLI Streaming Tool

```bash
# Build the CLI tool
go build -o whotalkie-stream ./cmd/whotalkie-stream

# High-quality stereo streaming
./whotalkie-stream -username "RadioBot" -channel "music" -bitrate 128000 -stereo

# Basic mono streaming  
./whotalkie-stream -username "BasicBot" -bitrate 32000 -stereo=false

# Test with custom settings
./whotalkie-stream -username "TestBot" -channel "test" -bitrate 64000 -duration 30

# Stream live internet radio with ffmpeg (Opus passthrough)
ffmpeg -i https://stream.zeno.fm/vgchxkqc998uv -f ogg -c:a libopus -b:a 64k -ac 2 - | \
    ./whotalkie-stream -username "LiveRadio" -channel "music" -bitrate 64000 -stereo -stdin

# Stream audio file through ffmpeg (Opus passthrough)  
ffmpeg -i input.mp3 -f ogg -c:a libopus -b:a 64k -ac 2 - | \
    ./whotalkie-stream -username "MusicBot" -channel "tunes" -bitrate 64000 -stereo -stdin

# Stream with mono audio (lower bandwidth, Opus passthrough)
ffmpeg -i https://stream.zeno.fm/vgchxkqc998uv -f ogg -c:a libopus -b:a 32k -ac 1 - | \
    ./whotalkie-stream -username "RadioMono" -channel "radio" -bitrate 32000 -stereo=false -stdin
```

### Using the Go Client Library

```go
import "whotalkie/pkg/client"

// Create and configure client
config := client.ClientConfig{
    ServerURL: "ws://localhost:8080/ws",
    Username:  "MyBot",
    Bitrate:   128000,  // High quality
    Channels:  2,       // Stereo
}

streamClient := client.NewStreamingClient(config)

// Connect and stream
streamClient.ConnectAndSetup(ctx)
streamClient.StreamForDuration(ctx, time.Minute*10, time.Second, 1024)
```

## 🏗️ Architecture

### Audio Pipeline
```
Microphone → 48kHz Resampling → WebCodecs Opus Encoder → WebSocket
                                                              ↓
Speaker ← Audio Mixing ← WebCodecs Opus Decoder ← WebSocket ← Server Relay
```

### Technology Stack
- **Backend**: Go with Gin web framework
- **Real-Time**: WebSocket for signaling + binary Opus packets
- **Audio**: WebCodecs API for native browser Opus support
- **Frontend**: Vanilla JavaScript with Web Audio API
- **Storage**: In-memory state management

## 📊 Performance Metrics

### Audio Quality
- **Sample Rate**: 48kHz (Opus native)
- **Bitrate**: 32kbps (optimized for voice)
- **Latency**: ~20ms encoding/decoding overhead
- **Compression**: ~90% bandwidth reduction vs PCM

### Bandwidth Usage
- **Opus Packets**: ~400-800 bytes per chunk
- **User Bandwidth**: ~16-32 KB/s per active user
- **Scaling**: Supports multiple simultaneous users efficiently

## 🛠️ Development

### Project Structure
```
whotalkie/
├── cmd/server/main.go           # Server entry point
├── web/templates/dashboard.html # Complete web client
├── NEXT_SESSION_NOTES.md        # Development roadmap
├── .claude-agent.md             # Project configuration
└── README.md                    # This file
```

### Key Components
- **WebSocket Handler** - Real-time event signaling
- **Opus Integration** - WebCodecs encoder/decoder management  
- **Audio Statistics** - Real-time performance monitoring
- **Multi-User Mixing** - Simultaneous audio stream playback
- **Channel Management** - User presence and channel switching
- **CLI Streaming Tool** - Publish-only client for external audio sources

### Browser Support Matrix
| Browser | WebCodecs | Opus Support | Status |
|---------|-----------|--------------|---------|
| Chrome 94+ | ✅ | ✅ | **Full Support** |
| Firefox 106+ | ✅ | ✅ | **Full Support** |
| Safari | ❌ | N/A | Not Supported |
| Edge 94+ | ✅ | ✅ | **Full Support** |

## 🎯 Use Cases

### **Friend Groups** 🎮
- Gaming communication with minimal latency
- Group coordination during activities
- Casual conversation with high audio quality

### **Teams & Organizations** 💼
- Remote team collaboration
- Conference calls with PTT functionality  
- Training sessions and briefings

### **Events & Coordination** 📻
- Event coordination with multiple channels
- Security and staff communication
- Live streaming coordination

## 🔧 Configuration

The server runs with sensible defaults:
- **HTTP Port**: 8080
- **Opus Settings**: 48kHz, 32kbps, mono
- **WebSocket**: Real-time event handling

Liveness and heartbeat expectations
----------------------------------

Clients must respond to WebSocket pings (or otherwise read from the
connection) so the server can detect unresponsive peers. The server sends
periodic pings (default every 20s) and expects a pong within a configured
timeout (default 40s). Clients that do not respond may be disconnected after
repeated failures.

If you're implementing a lightweight client, either:

- Ensure your WebSocket library processes control frames (pings/pongs). Many
    libraries do this automatically if you run a read loop.
- Or send periodic application-level heartbeat events (e.g., a JSON
    `"heartbeat"` event every ~20s); the server will treat missing heartbeats as a
    sign of a dead connection.

These defaults are configurable on the server for testing and can be tuned to
match client behavior in production.
- **Audio Buffer**: 2048 samples for latency/performance balance

## 📚 Client Library & Architecture

### Project Structure
```
pkg/client/                    # Reusable Go client library
├── types.go                   # Audio formats and capabilities
├── client.go                  # Core StreamClient implementation  
├── streaming.go               # High-level StreamingClient
└── README.md                  # Library documentation

cmd/whotalkie-stream/          # CLI application
├── main.go                    # CLI using the library
└── README.md                  # CLI documentation

examples/simple-client/        # Example library usage
└── main.go                    # Programmatic client example
```

### Client Types & Capabilities

**Web Clients (Browser)**
- Detected via User-Agent
- Restricted to 32kbps mono for PTT transmission  
- Can receive any audio format from server

**Streaming Clients (whotalkie-stream)**
- Advanced audio capabilities
- Support for 32k/64k/128kbps bitrates
- Full stereo audio transmission
- Publish-only mode for broadcasting

**Custom Clients** 
- Built using `whotalkie/pkg/client` library
- Configurable audio capabilities
- Event-driven architecture with custom handlers

### Building Custom Clients

```go
// See examples/simple-client/main.go for complete example
import "whotalkie/pkg/client"

// Minimal setup
config := client.ClientConfig{
    ServerURL: "ws://localhost:8080/ws",
    Username:  "CustomBot",
    Bitrate:   128000,    // High quality
    Channels:  2,         // Stereo
}

client := client.NewStreamingClient(config)
client.ConnectAndSetup(ctx)
```

## 📈 Roadmap

### Near-Term
- **Mobile Apps** - Native iOS/Android clients
- **Desktop Apps** - Electron/Tauri applications
- **Advanced Features** - Recording, noise suppression

### Long-Term  
- **P2P Audio** - WebRTC integration for direct connections
- **Scalability** - Multi-server deployment
- **Enterprise Features** - Authentication, admin controls

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch
3. Test with multiple browsers
4. Submit a pull request

## 📄 License

MIT License - see LICENSE file for details

## 🎙️ Why WhoTalkie?

Built for the modern web with professional-grade audio compression and real-time performance. No downloads, no setup - just open your browser and start talking.

**"Who's talking? We all are!"** 🚀

---

*For detailed development notes and technical implementation details, see [NEXT_SESSION_NOTES.md](NEXT_SESSION_NOTES.md)*