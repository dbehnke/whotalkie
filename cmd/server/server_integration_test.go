package main

import (
    "bytes"
    "context"
    "encoding/binary"
    "testing"
    "time"
)

// Smoke test: ensure startPublisherDemux writes raw Ogg bytes into the
// per-channel stream buffer and demux loop processes pages without panicking.
func TestServer_StartPublisherDemux_Smoke(t *testing.T) {
    s := NewServerWithOptions(1024 * 16)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Build a minimal Ogg page (OpusTags payload) and feed it to startPublisherDemux
    gp := uint64(48000)
    page := make([]byte, 0)
    page = append(page, []byte("OggS")...)
    page = append(page, 0)
    page = append(page, 0)
    g := make([]byte, 8)
    binary.LittleEndian.PutUint64(g, gp)
    page = append(page, g...)
    page = append(page, []byte{0, 0, 0, 1}...)
    page = append(page, []byte{0, 0, 0, 1}...)
    page = append(page, []byte{0, 0, 0, 0}...)
    // one segment containing "OpusTags" + 0
    payload := append([]byte("OpusTags"), 0)
    page = append(page, 1)
    page = append(page, byte(len(payload)))
    page = append(page, payload...)

    // Start the demux in background
    r := bytes.NewReader(page)
    go s.startPublisherDemux(ctx, "testchan", r)

    // Give the demux goroutine time to run and write to the stream buffer
    time.Sleep(200 * time.Millisecond)

    // Verify the stream buffer exists and has data
    b, ok := s.stateManager.GetStreamBuffer("testchan")
    if !ok {
        t.Fatalf("expected stream buffer for channel testchan")
    }
    rc := b.NewReader(context.Background())
    defer func() { _ = rc.Close() }()
    buf := make([]byte, 1024)
    n, err := rc.Read(buf)
    if err != nil && n == 0 {
        t.Fatalf("expected to read some bytes from stream buffer, err=%v", err)
    }
    if n == 0 {
        t.Fatalf("expected non-zero bytes in stream buffer")
    }
}
