package client

import "time"

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

// PTTEvent represents a WhoTalkie protocol event
type PTTEvent struct {
	Type      string                 `json:"type"`
	UserID    string                 `json:"user_id,omitempty"`
	ChannelID string                 `json:"channel_id,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// ClientConfig holds configuration for the streaming client
type ClientConfig struct {
	ServerURL   string
	Username    string
	Channel     string
	Bitrate     int
	Channels    int
	PublishOnly bool
	UserAgent   string
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

// CreateHighQualityCapabilities creates capabilities for high-quality streaming clients
func CreateHighQualityCapabilities(clientType, userAgent string, bitrate, channels int) ClientCapabilities {
	return ClientCapabilities{
		ClientType: clientType,
		UserAgent:  userAgent,
		SupportedFormats: []AudioFormat{
			{Codec: "opus", Bitrate: 32000, SampleRate: 48000, Channels: 1},
			{Codec: "opus", Bitrate: 64000, SampleRate: 48000, Channels: 1},
			{Codec: "opus", Bitrate: 128000, SampleRate: 48000, Channels: 1},
			{Codec: "opus", Bitrate: 64000, SampleRate: 48000, Channels: 2},
			{Codec: "opus", Bitrate: 128000, SampleRate: 48000, Channels: 2},
		},
		TransmitFormat: AudioFormat{
			Codec:      "opus",
			Bitrate:    bitrate,
			SampleRate: 48000,
			Channels:   channels,
		},
		SupportsVariableBR: true,
		SupportsStereo:     true,
	}
}