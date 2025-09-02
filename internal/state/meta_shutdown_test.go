package state

import (
	"context"
	"os"
	"testing"
	"time"

	"whotalkie/internal/types"
)

func TestMetaWorkerShutdownAndDrops(t *testing.T) {
	// Configure a tiny pool for the test
	if err := os.Setenv("META_BROADCAST_WORKERS", "1"); err != nil {
		t.Fatalf("failed to set env META_BROADCAST_WORKERS: %v", err)
	}
	if err := os.Setenv("META_BROADCAST_QUEUE_SIZE", "1"); err != nil {
		t.Fatalf("failed to set env META_BROADCAST_QUEUE_SIZE: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("META_BROADCAST_WORKERS"); err != nil {
			t.Fatalf("failed to unset env META_BROADCAST_WORKERS: %v", err)
		}
	}()
	defer func() {
		if err := os.Unsetenv("META_BROADCAST_QUEUE_SIZE"); err != nil {
			t.Fatalf("failed to unset env META_BROADCAST_QUEUE_SIZE: %v", err)
		}
	}()

	m := NewManager()

	// Start pool with background context
	m.StartMetaWorkerPool(context.Background())

	// Enqueue more items than the queue can hold to force drops
	for i := 0; i < 4; i++ {
		m.EnqueueMeta(&types.PTTEvent{Type: "meta"})
	}

	// Allow some time for worker to process
	time.Sleep(100 * time.Millisecond)

	// Shutdown manager which should close queue and wait for worker
	m.Shutdown()

	if got := m.MetaWorkerCount(); got != 0 {
		t.Fatalf("expected 0 meta workers after shutdown, got %d", got)
	}
	if got := m.MetaDropped(); got == 0 {
		t.Fatalf("expected some dropped meta events, got %d", got)
	}
}
