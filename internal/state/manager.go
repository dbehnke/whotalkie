package state

import (
	"context"
	"log"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"whotalkie/internal/stream"
	"whotalkie/internal/types"
)

type Manager struct {
	mu                    sync.RWMutex
	users                 map[string]*types.User
	channels              map[string]*types.Channel
	clients               map[string]*types.WebSocketConnection
	events                chan *types.PTTEvent
	// metaJobs is a bounded queue of metadata events processed by a fixed
	// worker pool to avoid unbounded goroutine creation when many meta
	// broadcasts occur.
	metaJobs              chan *types.PTTEvent
	metaWG                sync.WaitGroup
	// streamBuffers holds per-channel live Ogg raw stream buffers for HTTP proxying
	streamBuffers         map[string]*stream.Buffer
	droppedCriticalEvents int  // Track dropped critical events for monitoring
	isShuttingDown        bool // Flag to prevent race conditions during shutdown
}

// Event buffer configuration constants
const (
	DefaultEventBufferSize = 1000  // Increased from 100 for better throughput
	MaxEventBufferSize     = 10000 // Maximum buffer size for high-load scenarios
	// Meta worker defaults
	DefaultMetaWorkerCount = 4
	DefaultMetaQueueSize   = 100
)

func NewManager() *Manager {
	return &Manager{
		users:    make(map[string]*types.User),
		channels: make(map[string]*types.Channel),
		clients:  make(map[string]*types.WebSocketConnection),
	events:   make(chan *types.PTTEvent, DefaultEventBufferSize),
	streamBuffers: make(map[string]*stream.Buffer),
	}
}

// NewManagerWithBufferSize creates a manager with custom event buffer size
func NewManagerWithBufferSize(bufferSize int) *Manager {
	if bufferSize <= 0 {
		bufferSize = DefaultEventBufferSize
	}
	if bufferSize > MaxEventBufferSize {
		bufferSize = MaxEventBufferSize
	}

	return &Manager{
		users:    make(map[string]*types.User),
		channels: make(map[string]*types.Channel),
		clients:  make(map[string]*types.WebSocketConnection),
	events:   make(chan *types.PTTEvent, bufferSize),
	streamBuffers: make(map[string]*stream.Buffer),
	}
}

// StartMetaWorkerPool starts a fixed-size pool of workers that will process
// metadata broadcast jobs enqueued via EnqueueMeta. Worker/queue sizes can be
// configured via the META_BROADCAST_WORKERS and META_BROADCAST_QUEUE_SIZE
// environment variables. This method is idempotent.
func (m *Manager) StartMetaWorkerPool(ctx context.Context) {
	m.mu.Lock()
	if m.metaJobs != nil {
		m.mu.Unlock()
		return
	}

	workers := getEnvIntOrDefault("META_BROADCAST_WORKERS", DefaultMetaWorkerCount)
	queueSize := getEnvIntOrDefault("META_BROADCAST_QUEUE_SIZE", DefaultMetaQueueSize)

	m.metaJobs = make(chan *types.PTTEvent, queueSize)
	m.mu.Unlock()

	// Start worker goroutines
	for i := 0; i < workers; i++ {
		m.metaWG.Add(1)
		go m.runMetaWorker(ctx)
	}
}

// getEnvIntOrDefault parses an environment variable into an int, returning
// the provided default on error or if unset.
func getEnvIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if iv, err := strconv.Atoi(v); err == nil && iv > 0 {
			return iv
		}
	}
	return def
}

// runMetaWorker is the loop executed by each meta worker goroutine. Separated
// out to reduce cyclomatic complexity in StartMetaWorkerPool.
func (m *Manager) runMetaWorker(ctx context.Context) {
	defer m.metaWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-m.metaJobs:
			if !ok {
				return
			}
			m.BroadcastEvent(ev)
		}
	}
}

// EnqueueMeta submits a metadata event to the meta worker queue. It returns
// true if the event was queued, or false if the queue was full or the manager
// is shutting down. Non-blocking to avoid stalling producers.
func (m *Manager) EnqueueMeta(event *types.PTTEvent) bool {
	m.mu.RLock()
	if m.isShuttingDown || m.metaJobs == nil {
		m.mu.RUnlock()
		return false
	}
	metaJobs := m.metaJobs
	m.mu.RUnlock()

	select {
	case metaJobs <- event:
		return true
	default:
		// Drop on overflow to protect the system; track as a dropped critical
		// event for visibility (re-using the counter).
		m.mu.Lock()
		m.droppedCriticalEvents++
		m.mu.Unlock()
		log.Printf("WARNING: meta enqueue dropped due to full queue")
		return false
	}
}

// GetOrCreateStreamBuffer returns an existing buffer for the channel or creates one.
func (m *Manager) GetOrCreateStreamBuffer(channelID string) *stream.Buffer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.streamBuffers[channelID]; ok {
		return b
	}
	// default buffer size 1MB
	b := stream.NewBuffer(1<<20)
	m.streamBuffers[channelID] = b
	return b
}

// GetStreamBuffer returns the buffer if present
func (m *Manager) GetStreamBuffer(channelID string) (*stream.Buffer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.streamBuffers[channelID]
	return b, ok
}

// CloseStreamBuffer closes and removes the buffer for a channel
func (m *Manager) CloseStreamBuffer(channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.streamBuffers[channelID]; ok {
		_ = b.Close()
		delete(m.streamBuffers, channelID)
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

// SetChannelMeta stores stream metadata/comments (e.g., Vorbis comments) on the channel.
// This is used by the server to persist and periodically re-broadcast metadata for listeners.
func (m *Manager) SetChannelMeta(channelID string, comments string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.channels[channelID]; ok {
		ch.Description = comments
	}
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
	// Check if we're shutting down first
	m.mu.RLock()
	if m.isShuttingDown {
		m.mu.RUnlock()
		log.Printf("Discarding event %s during shutdown", event.Type)
		return
	}
	m.mu.RUnlock()

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

	// Calculate event buffer utilization
	eventBufferLen := len(m.events)
	eventBufferCap := cap(m.events)

	return types.ServerStats{
		TotalUsers:            len(m.users),
		ActiveUsers:           activeUsers,
		TotalChannels:         len(m.channels),
		ActiveChannels:        activeChannels,
		ConnectedClients:      len(m.clients),
		DroppedCriticalEvents: m.droppedCriticalEvents,
		EventBufferLength:     eventBufferLen,
		EventBufferCapacity:   eventBufferCap,
	}
}

// Shutdown gracefully closes the event channel and cleans up resources
func (m *Manager) Shutdown() {
	// First, mark we're shutting down and take ownership of the metaJobs
	// channel so we can close it and wait for workers without holding the
	// manager lock (workers may call BroadcastEvent which acquires locks).
	m.mu.Lock()
	if m.isShuttingDown {
		m.mu.Unlock()
		log.Printf("Shutdown already in progress, ignoring duplicate call")
		return
	}
	m.isShuttingDown = true

	// Steal the metaJobs channel reference so we can close it without holding
	// the lock and avoid races with EnqueueMeta (which checks isShuttingDown).
	var metaJobs chan *types.PTTEvent
	if m.metaJobs != nil {
		metaJobs = m.metaJobs
		m.metaJobs = nil
	}
	m.mu.Unlock()

	// Close metaJobs and wait for workers to exit. Do this before closing the
	// primary events channel because workers call BroadcastEvent which will
	// attempt to send into m.events; closing m.events first would cause a
	// panic in the workers.
	if metaJobs != nil {
		close(metaJobs)
		m.metaWG.Wait()
	}

	// Now acquire lock to close remaining resources and the events channel.
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close event channel to signal broadcast goroutine to stop
	if m.events != nil {
		close(m.events)
		m.events = nil
	}

	// Close all client Send channels safely
	for userID, client := range m.clients {
		// Check if channel is already closed by trying a non-blocking receive
		select {
		case <-client.Send:
			// Channel is already closed
		default:
			// Channel is open, safe to close
			close(client.Send)
		}
		delete(m.clients, userID)
	}

	// Close stream buffers
	for ch, buf := range m.streamBuffers {
		if buf != nil {
			_ = buf.Close()
		}
		delete(m.streamBuffers, ch)
	}

	log.Printf("State manager shutdown complete")
}
