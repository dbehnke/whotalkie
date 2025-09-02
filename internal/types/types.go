package types

import (
	"time"

	"github.com/coder/websocket"
)

// AudioFormat represents the audio configuration capabilities and preferences
type AudioFormat struct {
	Codec      string `json:"codec"`       // "opus", "pcm", etc.
	Bitrate    int    `json:"bitrate"`     // bits per second (e.g., 32000, 64000, 128000)
	SampleRate int    `json:"sample_rate"` // samples per second (e.g., 48000)
	Channels   int    `json:"channels"`    // 1 for mono, 2 for stereo
}

// ClientCapabilities represents what audio formats and features a client supports
type ClientCapabilities struct {
	ClientType          string        `json:"client_type"`           // "web", "whotalkie-stream", "custom"
	UserAgent          string        `json:"user_agent,omitempty"`  // Browser/client user agent
	SupportedFormats   []AudioFormat `json:"supported_formats"`     // Formats client can receive
	TransmitFormat     AudioFormat   `json:"transmit_format"`       // Format client will transmit in
	SupportsVariableBR bool          `json:"supports_variable_br"`  // Can handle variable bitrate
	SupportsStereo     bool          `json:"supports_stereo"`       // Can handle stereo audio
}

// DefaultWebClientCapabilities returns standard web client audio capabilities
func DefaultWebClientCapabilities() ClientCapabilities {
	webFormat := AudioFormat{
		Codec:      "opus",
		Bitrate:    32000, // 32kbps
		SampleRate: 48000,
		Channels:   1, // Mono only
	}
	
	return ClientCapabilities{
		ClientType:          "web",
		SupportedFormats:   []AudioFormat{webFormat},
		TransmitFormat:     webFormat,
		SupportsVariableBR: false,
		SupportsStereo:     false,
	}
}

type User struct {
	ID           string             `json:"id"`
	Username     string             `json:"username"`
	Channel      string             `json:"channel"`
	IsActive     bool               `json:"is_active"`
	PublishOnly  bool               `json:"publish_only,omitempty"`
	IsWebClient  bool               `json:"is_web_client,omitempty"`
	Capabilities ClientCapabilities `json:"capabilities,omitempty"`
}

type SpeakerState struct {
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	StartTime time.Time `json:"start_time"`
	IsTalking bool      `json:"is_talking"`
}

type Channel struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Users            []User                  `json:"users"`
	ActiveSpeakers   map[string]SpeakerState `json:"active_speakers"`
	CreatedAt        time.Time               `json:"created_at"`
	MaxUsers         int                     `json:"max_users"`
	IsActive         bool                    `json:"is_active"`
	Description      string                  `json:"description,omitempty"`
	PublishOnlyCount int                     `json:"publish_only_count,omitempty"`
}

type PTTEvent struct {
	Type      string                 `json:"type"`
	UserID    string                 `json:"user_id"`
	ChannelID string                 `json:"channel_id"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

type WebSocketConnection struct {
	Conn   *websocket.Conn
	UserID string
	Send   chan []byte
	// LastAppHeartbeat points to a unix-nanoseconds timestamp updated when the
	// client sends an application-level heartbeat event. It's optional and
	// used by the server to consider application heartbeats as evidence of
	// liveness when control-frame pongs may be lost by intermediaries.
	LastAppHeartbeat *int64
}

type ServerState struct {
	Users    map[string]*User                `json:"users"`
	Channels map[string]*Channel             `json:"channels"`
	Clients  map[string]*WebSocketConnection `json:"-"`
}

type PTTEventType string

const (
	EventUserJoin        PTTEventType = "user_join"
	EventUserLeave       PTTEventType = "user_leave"
	EventPTTStart        PTTEventType = "ptt_start"
	EventPTTEnd          PTTEventType = "ptt_end"
	EventChannelJoin     PTTEventType = "channel_join"
	EventChannelLeave    PTTEventType = "channel_leave"
	EventAudioData       PTTEventType = "audio_data"
	EventHeartbeat       PTTEventType = "heartbeat"
	EventCapabilityNego  PTTEventType = "capability_negotiation"
)

// IsCritical returns true if the event type is critical and should be delivered with timeout protection
func (e PTTEventType) IsCritical() bool {
	switch e {
	case EventPTTStart, EventPTTEnd, EventUserJoin, EventUserLeave, EventChannelJoin, EventChannelLeave:
		return true
	default:
		return false
	}
}

type ServerStats struct {
	TotalUsers            int `json:"total_users"`
	ActiveUsers           int `json:"active_users"`
	TotalChannels         int `json:"total_channels"`
	ActiveChannels        int `json:"active_channels"`
	ConnectedClients      int `json:"connected_clients"`
	DroppedCriticalEvents int `json:"dropped_critical_events"`
	EventBufferLength     int `json:"event_buffer_length"`
	EventBufferCapacity   int `json:"event_buffer_capacity"`
	MetaWorkerCount       int `json:"meta_worker_count"`
	MetaDroppedEvents     int `json:"meta_dropped_events"`
}
