package state

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"whotalkie/internal/types"
)

type Manager struct {
	mu       sync.RWMutex
	users    map[string]*types.User
	channels map[string]*types.Channel
	clients  map[string]*types.WebSocketConnection
	events   chan *types.PTTEvent
}

func NewManager() *Manager {
	return &Manager{
		users:    make(map[string]*types.User),
		channels: make(map[string]*types.Channel),
		clients:  make(map[string]*types.WebSocketConnection),
		events:   make(chan *types.PTTEvent, 100),
	}
}

func (m *Manager) AddUser(user *types.User) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[user.ID] = user
}

func (m *Manager) RemoveUser(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if user, exists := m.users[userID]; exists {
		if user.Channel != "" {
			m.removeUserFromChannel(userID, user.Channel)
		}
		delete(m.users, userID)
	}
}

func (m *Manager) GetUser(userID string) (*types.User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, exists := m.users[userID]
	return user, exists
}

func (m *Manager) UpdateUser(user *types.User) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[user.ID] = user
}

func (m *Manager) GetAllUsers() []*types.User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	users := make([]*types.User, 0, len(m.users))
	for _, user := range m.users {
		users = append(users, user)
	}
	
	// Sort users by username for consistent ordering
	sort.Slice(users, func(i, j int) bool {
		return users[i].Username < users[j].Username
	})
	
	return users
}

func (m *Manager) AddClient(userID string, conn *types.WebSocketConnection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[userID] = conn
}

func (m *Manager) RemoveClient(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, userID)
}

func (m *Manager) GetClient(userID string) (*types.WebSocketConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, exists := m.clients[userID]
	return client, exists
}

func (m *Manager) GetAllClients() map[string]*types.WebSocketConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	clients := make(map[string]*types.WebSocketConnection)
	for k, v := range m.clients {
		clients[k] = v
	}
	return clients
}

func (m *Manager) CreateChannel(channelID, name string) *types.Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	channel := &types.Channel{
		ID:               channelID,
		Name:             name,
		Users:            []types.User{},
		ActiveSpeakers:   make(map[string]types.SpeakerState),
		CreatedAt:        time.Now(),
		MaxUsers:         50,
		IsActive:         true,
		Description:      "",
		PublishOnlyCount: 0,
	}
	
	m.channels[channelID] = channel
	return channel
}

func (m *Manager) GetChannel(channelID string) (*types.Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	channel, exists := m.channels[channelID]
	return channel, exists
}

func (m *Manager) GetOrCreateChannel(channelID, name string) *types.Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Double-checked locking pattern to prevent race conditions
	if channel, exists := m.channels[channelID]; exists {
		return channel
	}
	
	// Create channel while holding the lock
	channel := &types.Channel{
		ID:               channelID,
		Name:             name,
		Users:            []types.User{},
		ActiveSpeakers:   make(map[string]types.SpeakerState),
		CreatedAt:        time.Now(),
		MaxUsers:         50,
		IsActive:         true,
		Description:      "",
		PublishOnlyCount: 0,
	}
	
	m.channels[channelID] = channel
	return channel
}

func (m *Manager) GetAllChannels() []*types.Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	channels := make([]*types.Channel, 0, len(m.channels))
	for _, channel := range m.channels {
		channels = append(channels, channel)
	}
	
	// Sort channels by name for consistent ordering
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Name < channels[j].Name
	})
	
	return channels
}

func (m *Manager) JoinChannel(userID, channelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	user, userExists := m.users[userID]
	if !userExists {
		return ErrUserNotFound
	}
	
	channel, channelExists := m.channels[channelID]
	if !channelExists {
		return ErrChannelNotFound
	}
	
	if user.Channel != "" && user.Channel != channelID {
		m.removeUserFromChannel(userID, user.Channel)
	}
	
	user.Channel = channelID
	user.IsActive = true
	
	found := false
	for i, channelUser := range channel.Users {
		if channelUser.ID == userID {
			// Update publish-only count if status changed
			if channelUser.PublishOnly && !user.PublishOnly {
				channel.PublishOnlyCount--
			} else if !channelUser.PublishOnly && user.PublishOnly {
				channel.PublishOnlyCount++
			}
			channel.Users[i] = *user
			found = true
			break
		}
	}
	
	if !found {
		channel.Users = append(channel.Users, *user)
		if user.PublishOnly {
			channel.PublishOnlyCount++
		}
	}
	
	return nil
}

func (m *Manager) LeaveChannel(userID, channelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.removeUserFromChannel(userID, channelID)
}

func (m *Manager) removeUserFromChannel(userID, channelID string) error {
	user, userExists := m.users[userID]
	if !userExists {
		return ErrUserNotFound
	}
	
	channel, channelExists := m.channels[channelID]
	if !channelExists {
		return ErrChannelNotFound
	}
	
	for i, channelUser := range channel.Users {
		if channelUser.ID == userID {
			if channelUser.PublishOnly {
				channel.PublishOnlyCount--
			}
			channel.Users = append(channel.Users[:i], channel.Users[i+1:]...)
			break
		}
	}
	
	user.Channel = ""
	user.IsActive = false
	
	return nil
}

func (m *Manager) BroadcastEvent(event *types.PTTEvent) {
	// Validate event type before conversion to prevent issues with invalid event types
	eventType := types.PTTEventType(event.Type)
	isCritical := false
	
	// Only check criticality for known event types to prevent panics
	switch eventType {
	case types.EventPTTStart, types.EventPTTEnd, types.EventUserJoin, 
		 types.EventUserLeave, types.EventChannelJoin, types.EventChannelLeave,
		 types.EventAudioData, types.EventHeartbeat:
		isCritical = eventType.IsCritical()
	default:
		// Unknown event type - treat as non-critical for safety
		log.Printf("WARNING: Unknown event type received: %s", event.Type)
		isCritical = false
	}
	
	if isCritical {
		// Use context with timeout for critical events to avoid deadlocks
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		
		select {
		case m.events <- event:
			// Event delivered successfully
		case <-ctx.Done():
			// Log dropped critical event - this should be monitored
			log.Printf("WARNING: Critical event dropped due to timeout: %s from user %s", 
				event.Type, event.UserID)
		}
	} else {
		// Non-critical events use non-blocking approach
		select {
		case m.events <- event:
		default:
			// Silent drop for non-critical events (heartbeat, etc.)
		}
	}
}

func (m *Manager) GetEventChannel() <-chan *types.PTTEvent {
	return m.events
}

func (m *Manager) GetStats() types.ServerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	activeUsers := 0
	for _, user := range m.users {
		if user.IsActive {
			activeUsers++
		}
	}
	
	activeChannels := 0
	for _, channel := range m.channels {
		if channel.IsActive && len(channel.Users) > 0 {
			activeChannels++
		}
	}
	
	return types.ServerStats{
		TotalUsers:      len(m.users),
		ActiveUsers:     activeUsers,
		TotalChannels:   len(m.channels),
		ActiveChannels:  activeChannels,
		ConnectedClients: len(m.clients),
	}
}

// Shutdown gracefully closes the event channel and cleans up resources
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Close event channel to signal broadcast goroutine to stop
	close(m.events)
	
	// Close all client connections
	for _, client := range m.clients {
		close(client.Send)
	}
	
	log.Printf("State manager shutdown complete")
}