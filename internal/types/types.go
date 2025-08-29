package types

import (
	"time"

	"nhooyr.io/websocket"
)

type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Channel     string `json:"channel"`
	IsActive    bool   `json:"is_active"`
	PublishOnly bool   `json:"publish_only,omitempty"`
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
}

type ServerState struct {
	Users    map[string]*User               `json:"users"`
	Channels map[string]*Channel            `json:"channels"`
	Clients  map[string]*WebSocketConnection `json:"-"`
}

type PTTEventType string

const (
	EventUserJoin     PTTEventType = "user_join"
	EventUserLeave    PTTEventType = "user_leave"
	EventPTTStart     PTTEventType = "ptt_start"
	EventPTTEnd       PTTEventType = "ptt_end"
	EventChannelJoin  PTTEventType = "channel_join"
	EventChannelLeave PTTEventType = "channel_leave"
	EventAudioData    PTTEventType = "audio_data"
	EventHeartbeat    PTTEventType = "heartbeat"
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
	TotalUsers       int `json:"total_users"`
	ActiveUsers      int `json:"active_users"`
	TotalChannels    int `json:"total_channels"`
	ActiveChannels   int `json:"active_channels"`
	ConnectedClients int `json:"connected_clients"`
}