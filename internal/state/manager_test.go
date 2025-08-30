package state_test

import (
	"testing"
	"time"

	"whotalkie/internal/state"
	"whotalkie/internal/types"
)

func TestJoinLeaveChannel_PublishOnlyCount(t *testing.T) {
	m := state.NewManager()

	// Create two users
	u1 := &types.User{ID: "u1", Username: "Alice"}
	u2 := &types.User{ID: "u2", Username: "Bob", PublishOnly: true}

	m.AddUser(u1)
	m.AddUser(u2)

	// Create channel
	ch := m.GetOrCreateChannel("room1", "Room 1")
	if ch == nil {
		t.Fatalf("expected channel to be created")
	}

	// Join both users
	if err := m.JoinChannel("u1", "room1"); err != nil {
		t.Fatalf("failed to join u1: %v", err)
	}
	if err := m.JoinChannel("u2", "room1"); err != nil {
		t.Fatalf("failed to join u2: %v", err)
	}

	// Check publish-only count
	ch2, ok := m.GetChannel("room1")
	if !ok {
		t.Fatalf("channel missing after join")
	}
	if ch2.PublishOnlyCount != 1 {
		t.Fatalf("expected PublishOnlyCount=1, got %d", ch2.PublishOnlyCount)
	}

	// User 2 leaves
	if err := m.LeaveChannel("u2", "room1"); err != nil {
		t.Fatalf("failed to leave u2: %v", err)
	}

	ch3, _ := m.GetChannel("room1")
	if ch3.PublishOnlyCount != 0 {
		t.Fatalf("expected PublishOnlyCount=0 after leave, got %d", ch3.PublishOnlyCount)
	}
}

func TestJoinChannel_UserMovesChannels(t *testing.T) {
	m := state.NewManager()

	// Users and channels
	u := &types.User{ID: "u3", Username: "Carol"}
	m.AddUser(u)
	m.GetOrCreateChannel("a", "A")
	m.GetOrCreateChannel("b", "B")

	// Join channel a
	if err := m.JoinChannel("u3", "a"); err != nil {
		t.Fatalf("join a failed: %v", err)
	}

	// Now join b (should move from a to b)
	if err := m.JoinChannel("u3", "b"); err != nil {
		t.Fatalf("join b failed: %v", err)
	}

	// Verify user channel
	user, ok := m.GetUser("u3")
	if !ok || user.Channel != "b" {
		t.Fatalf("expected user in channel b, got %v", user)
	}

	// verify channel a does not contain user
	ca, _ := m.GetChannel("a")
	for _, cu := range ca.Users {
		if cu.ID == "u3" {
			t.Fatalf("user still in channel a")
		}
	}

	// verify channel b has the user
	cb, _ := m.GetChannel("b")
	found := false
	for _, cu := range cb.Users {
		if cu.ID == "u3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user not found in channel b")
	}
}

func TestGetStats_BasicCounts(t *testing.T) {
	m := state.NewManager()
	m.AddUser(&types.User{ID: "s1", Username: "S1", IsActive: true})
	m.AddUser(&types.User{ID: "s2", Username: "S2", IsActive: true})
	m.GetOrCreateChannel("alpha", "Alpha")
	if err := m.JoinChannel("s1", "alpha"); err != nil {
		t.Fatalf("Failed to join channel: %v", err)
	}

	stats := m.GetStats()
	if stats.TotalUsers != 2 {
		t.Fatalf("expected total users 2 got %d", stats.TotalUsers)
	}
	if stats.ActiveUsers < 1 {
		t.Fatalf("expected at least 1 active user got %d", stats.ActiveUsers)
	}
	if stats.TotalChannels < 1 {
		t.Fatalf("expected at least 1 channel got %d", stats.TotalChannels)
	}

	// Sleep briefly to ensure createdAt times differ if needed by logic
	time.Sleep(10 * time.Millisecond)
}
