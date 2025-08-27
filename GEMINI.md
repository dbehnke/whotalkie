# WhoTalkie - Gemini Agent Configuration

## Project Overview

**WhoTalkie** is a Push-to-Talk (PTT) application designed for friend groups to communicate with real-time voice. Named as a play on "Who Cares" + "Walkie-Talkie" + "Who's Talking".

## Core Architecture

### Technology Stack
- **Language**: Go (chosen for excellent concurrency, networking, and simplicity)
- **WebSocket Library**: `nhooyr.io/websocket` (modern, lightweight alternative to Gorilla)
- **Web Framework**: Gin (for serving web client, dashboard, and REST API)
- **State Storage**: In-memory with Go sync primitives (no Redis dependency for Go-native approach)
- **Audio Codec**: Opus (clients handle encoding/decoding, server does pass-through routing)

### Network Architecture
**Dual-Protocol Design:**
- **WebSocket (TCP)**: Control plane - PTT events, user presence, channel management, signaling
- **UDP**: Data plane - Raw audio packet transmission for lowest latency
- **HTTP**: Web dashboard, static files, REST API

### Audio Strategy
**Pass-Through Proxy Approach (like Discord):**
- Server forwards Opus packets without decoding/re-encoding (no CGO complications)
- Clients handle Opus encoding/decoding
- Web clients use native browser Web Audio API for mixing
- Server acts as intelligent packet router, not audio processor

## Project Structure

```
whotalkie/
├── cmd/
│   └── server/
│       └── main.go                 # Server entry point
├── internal/
│   ├── server/                     # Core server logic
│   │   ├── server.go               # Main server struct
│   │   ├── websocket.go            # WebSocket handling
│   │   ├── udp.go                  # UDP audio routing
│   │   └── handlers.go             # HTTP/API handlers
│   ├── types/                      # Core data structures
│   │   └── types.go                # User, Channel, PTTEvent types
│   └── state/                      # In-memory state management
│       └── state.go                # Thread-safe state operations
├── web/
│   ├── templates/                  # HTML templates
│   │   ├── dashboard.html          # Admin dashboard
│   │   └── client.html             # Web PTT client
│   └── static/                     # CSS, JS, assets
│       ├── dashboard.js            # Dashboard functionality
│       └── client.js               # Web PTT client
├── pkg/
│   └── protocol/                   # Network protocol definitions
│       └── packets.go              # UDP packet structures
├── configs/
│   └── config.yaml                 # Server configuration
├── docs/
│   └── architecture.md             # Architecture documentation
├── Makefile                        # Build commands
├── go.mod                          # Go dependencies
└── .claude-agent                   # This file
```

## Core Data Structures

### Key Types
```go
type RadioID uint32      // Unique user identifier
type ChannelID uint16    // Channel identifier

type User struct {
    RadioID     RadioID
    Alias       string      // Human-readable name
    ChannelID   ChannelID
    LastSeen    time.Time
    IsActive    bool
}

type Channel struct {
    ID              ChannelID
    ActiveSpeakers  map[RadioID]*SpeakerState
    Subscribers     map[RadioID]*User
    LastActivity    time.Time
}

type PTTEvent struct {
    Type        string      // "ptt_start", "ptt_end", "user_join", "user_leave"
    RadioID     RadioID
    ChannelID   ChannelID
    Alias       string
    Timestamp   int64
}
```

### UDP Audio Packet Format
```go
type AudioPacket struct {
    RadioID     uint32    // 4 bytes - who's speaking
    ChannelID   uint16    // 2 bytes - which channel
    Sequence    uint32    // 4 bytes - packet ordering
    Timestamp   uint64    // 8 bytes - timing
    AudioData   []byte    // Variable - Opus encoded audio
}
```

## Network Protocol

### WebSocket Messages (JSON)
- **ptt_start**: User begins transmitting
- **ptt_end**: User stops transmitting
- **user_join**: User joins channel
- **user_leave**: User leaves channel
- **channel_state**: Current channel information

### UDP Audio Flow
1. Client encodes audio to Opus
2. Client sends UDP packets to server
3. Server validates RadioID/ChannelID
4. Server forwards packets to all channel subscribers
5. Clients decode Opus and play audio

### NAT Traversal
- **Primary**: Client-initiated UDP (client sends first packet to register endpoint)
- **Fallback**: UDP-over-WebSocket for problematic networks
- **Detection**: UDP registration timeout (5 seconds) triggers fallback

## Audio Processing

### Client-Side (Web Browser)
```javascript
// Recording with MediaRecorder (native Opus encoding)
const mediaRecorder = new MediaRecorder(stream, {
    mimeType: 'audio/webm; codecs=opus',
    audioBitsPerSecond: 64000
});

// Playback with Web Audio API mixing
const audioContext = new AudioContext();
const mixerNode = audioContext.createGain();
// Mix multiple incoming Opus streams
```

### Server-Side (Pass-Through)
- No audio processing/decoding required
- Pure packet routing and validation
- Maintains low latency and simplicity

## State Management

### In-Memory Storage (Go-Native)
```go
type PTTState struct {
    channels        map[ChannelID]*Channel
    users          map[RadioID]*User
    connections    map[*websocket.Conn]*ClientConnection
    channelMutex   map[ChannelID]*sync.RWMutex
    globalMutex    sync.RWMutex
}
```

### Scaling Strategy
- **Phase 1**: Pure in-memory (20 users)
- **Phase 2**: Add BadgerDB for persistence (100 users)
- **Phase 3**: Add Redis for multi-server (500+ users)

## Development Guidelines

### Dependencies
```go
// Minimal required dependencies
require (
    github.com/gin-gonic/gin v1.9.1
    nhooyr.io/websocket v1.8.7
)
```

### Code Style
- Follow standard Go conventions
- Use interfaces for testability
- Prefer composition over inheritance
- Handle errors explicitly
- Use context.Context for cancellation

### Testing Strategy
- Unit tests for core logic
- Integration tests for WebSocket/UDP
- Load testing for concurrent users
- Browser testing for web client

## Key Features

### Core PTT Functionality
- Push-to-talk with radio ID and alias system
- Channel-based group communication
- Real-time audio streaming with low latency
- Web-based and native client support

### Web Interface
- Browser-based PTT client using Web Audio API
- Admin dashboard for server monitoring
- Channel management interface
- User presence indicators

### Advanced Features (Future)
- Private one-on-one conversations
- Text chat integration
- Recording bot support
- Mobile client APIs

## Development Commands

```bash
# Development
make run          # Start server in development mode
make test         # Run all tests
make build        # Build production binary

# Server endpoints
GET  /            # Dashboard
GET  /client      # Web PTT client  
GET  /api/v1/*    # REST API
GET  /ws          # WebSocket upgrade
UDP  :8081        # Audio packets
```

## Configuration

### Server Configuration
```yaml
server:
  http_port: 8080
  udp_port: 8081
  max_users: 100
  max_channels: 50

audio:
  sample_rate: 48000
  frame_size_ms: 20
  bitrate: 64000
```

## Security Considerations

### Authentication
- RadioID validation
- WebSocket origin checking
- Rate limiting on connections

### Network Security
- Packet validation
- Prevent UDP spoofing
- HTTPS for production deployment

## Deployment

### Single Binary Deployment
- Self-contained Go binary
- Embedded web assets
- No external dependencies
- Docker container support

### Production Considerations
- Reverse proxy (nginx)
- HTTPS termination
- Log aggregation
- Monitoring and metrics

## Known Challenges & Solutions

### CGO Complexity
- **Problem**: Opus libraries typically require CGO
- **Solution**: Pass-through proxy avoids server-side audio processing

### Cross-Compilation
- **Problem**: CGO complicates cross-platform builds  
- **Solution**: Pure Go server, clients handle Opus locally

### NAT Traversal
- **Problem**: UDP behind NAT/firewalls
- **Solution**: Client-initiated UDP with WebSocket fallback

### Scaling
- **Problem**: In-memory state limits scaling
- **Solution**: Horizontal scaling plan with external storage

## Success Metrics

### Technical Goals
- <50ms audio latency
- Support 20+ concurrent users
- 99.9% uptime
- Cross-platform compatibility

### User Experience Goals
- One-click web client access
- Intuitive PTT interface
- Clear audio quality
- Reliable connections

---

This configuration should guide development of a high-quality, scalable PTT application that prioritizes simplicity, performance, and user experience while maintaining clean, Go-idiomatic code.