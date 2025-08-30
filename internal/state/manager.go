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
	mu                    sync.RWMutex
	users                 map[string]*types.User
	channels              map[string]*types.Channel
	clients               map[string]*types.WebSocketConnection
	events                chan *types.PTTEvent
	droppedCriticalEvents int // Track dropped critical events for monitoring
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
			if err := m.removeUserFromChannel(userID, user.Channel); err != nil {
				log.Printf("Failed to remove user %s from channel %s: %v", userID, user.Channel, err)
			}
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
	client, exists := m.clients[userID]
	if exists {
		delete(m.clients, userID)
	}
	m.mu.Unlock()

	// Close Send channel outside the lock to avoid deadlocks.
	if exists && client != nil {
		// Closing a channel that may already be closed can panic; ensure
		// that only the manager is responsible for closing client Send
		// channels to reduce risk of double-close. Callers should remove
		// clients via RemoveClient and not close the channel themselves.
		close(client.Send)
	}
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

// updatePublishOnlyCount updates the publish-only count when user status changes
func (m *Manager) updatePublishOnlyCount(channel *types.Channel, oldUser *types.User, newUser *types.User) {
	if oldUser.PublishOnly && !newUser.PublishOnly {
		channel.PublishOnlyCount--
	} else if !oldUser.PublishOnly && newUser.PublishOnly {
		channel.PublishOnlyCount++
	}
}

// updateExistingUserInChannel updates an existing user in the channel
func (m *Manager) updateExistingUserInChannel(channel *types.Channel, user *types.User, userID string) bool {
	for i, channelUser := range channel.Users {
		if channelUser.ID == userID {
			m.updatePublishOnlyCount(channel, &channelUser, user)
			channel.Users[i] = *user
			return true
		}
	}
	return false
}

// addNewUserToChannel adds a new user to the channel
func (m *Manager) addNewUserToChannel(channel *types.Channel, user *types.User) {
	channel.Users = append(channel.Users, *user)
	if user.PublishOnly {
		channel.PublishOnlyCount++
	}
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
		if err := m.removeUserFromChannel(userID, user.Channel); err != nil {
			log.Printf("Failed to remove user %s from previous channel %s: %v", userID, user.Channel, err)
		}
	}

	user.Channel = channelID
	user.IsActive = true

	if !m.updateExistingUserInChannel(channel, user, userID) {
		m.addNewUserToChannel(channel, user)
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
			// Log and track dropped critical event for monitoring
			m.mu.Lock()
			m.droppedCriticalEvents++
			m.mu.Unlock()

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
		TotalUsers:            len(m.users),
		ActiveUsers:           activeUsers,
		TotalChannels:         len(m.channels),
		ActiveChannels:        activeChannels,
		ConnectedClients:      len(m.clients),
		DroppedCriticalEvents: m.droppedCriticalEvents,
	}
}

// Shutdown gracefully closes the event channel and cleans up resources
func (m *Manager) Shutdown() {
	m.mu.Lock()

	// Close event channel to signal broadcast goroutine to stop
	close(m.events)

	// Snapshot client connections to avoid deadlocks
	clients := make([]*types.WebSocketConnection, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.mu.Unlock()

	// Close all client Send channels outside the lock to avoid deadlocks
	for _, client := range clients {
		close(client.Send)
	}

	log.Printf("State manager shutdown complete")
}
