package stream

import (
    "context"
    "io"
    "sync"
    "time"
)

// Buffer is a simple in-memory circular buffer that retains the last N bytes
// and allows callers to open blocking readers that stream current + future data.
type Buffer struct {
    mu    sync.Mutex
    cond  *sync.Cond
    buf   []byte
    max   int
    closed bool
}

// NewBuffer creates a new Buffer with a maximum size in bytes.
func NewBuffer(maxBytes int) *Buffer {
    b := &Buffer{max: maxBytes}
    b.cond = sync.NewCond(&b.mu)
    return b
}

// Write appends data to the buffer, truncating oldest bytes if necessary.
func (b *Buffer) Write(p []byte) (int, error) {
    b.mu.Lock()
    defer b.mu.Unlock()
    if b.closed {
        return 0, io.ErrClosedPipe
    }

    b.buf = append(b.buf, p...)
    if len(b.buf) > b.max {
        // drop oldest
        excess := len(b.buf) - b.max
        b.buf = b.buf[excess:]
    }
    b.cond.Broadcast()
    return len(p), nil
}

// Close marks the buffer as closed and wakes readers.
func (b *Buffer) Close() error {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.closed = true
    b.cond.Broadcast()
    return nil
}

// Reader is an io.ReadCloser that streams current buffer contents and waits
// for new data until the provided context is canceled.
type Reader struct {
    buf     *Buffer
    ctx     context.Context
    offset  int
    closed  bool
}

func (r *Reader) Read(p []byte) (int, error) {
    r.buf.mu.Lock()
    defer r.buf.mu.Unlock()

    for {
        if r.offset < len(r.buf.buf) {
            n := copy(p, r.buf.buf[r.offset:])
            r.offset += n
            return n, nil
        }

        if r.buf.closed {
            return 0, io.EOF
        }

        // Check context
        select {
        case <-r.ctx.Done():
            return 0, r.ctx.Err()
        default:
        }

        // Wait for new data or close
        waiter := make(chan struct{})
        go func() {
            r.buf.cond.Wait()
            close(waiter)
        }()

        r.buf.mu.Unlock()
        select {
        case <-r.ctx.Done():
            r.buf.mu.Lock()
            return 0, r.ctx.Err()
        case <-waiter:
            // re-acquire and loop
            r.buf.mu.Lock()
        }
    }
}

func (r *Reader) Close() error {
    if r.closed {
        return nil
    }
    r.closed = true
    return nil
}

// NewReader returns an io.ReadCloser that streams current buffer contents
// and blocks for future writes until ctx is done.
func (b *Buffer) NewReader(ctx context.Context) io.ReadCloser {
    return &Reader{buf: b, ctx: ctx, offset: 0}
}

// Helper: write bytes with a small timeout context to avoid blocking callers
func (b *Buffer) WriteWithTimeout(p []byte, timeout time.Duration) (int, error) {
    done := make(chan struct{})
    var n int
    var err error
    go func() {
        n, err = b.Write(p)
        close(done)
    }()

    select {
    case <-done:
        return n, err
    case <-time.After(timeout):
        return 0, io.ErrNoProgress
    }
}
