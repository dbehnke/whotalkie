package client

import (
    "bytes"
    "context"
    "encoding/binary"
    "io"
    "testing"
    "strings"

    "whotalkie/internal/oggdemux"
)

// fakeStreamClient embeds StreamingClient but overrides SendMeta for test
type fakeStreamClient struct {
    *StreamingClient
    sent []string
}

func (f *fakeStreamClient) SendMeta(ctx context.Context, comments string) error {
    f.sent = append(f.sent, comments)
    return nil
}

// Override StartStreaming/StopStreaming/SendOpusData to avoid network calls in tests
func (f *fakeStreamClient) StartStreaming(ctx context.Context) error {
    f.isStreaming = true
    return nil
}

func (f *fakeStreamClient) StopStreaming(ctx context.Context) error {
    f.isStreaming = false
    return nil
}

func (f *fakeStreamClient) SendOpusData(ctx context.Context, opusData []byte) error {
    // No-op in test; we're only interested in meta sends
    return nil
}

// helper to craft an OpusTags packet: OpusTags + vendor_len + vendor + list_len + comment_len + comment
func makeOpusTagsPacket(title string) []byte {
    var b bytes.Buffer
    b.WriteString("OpusTags")
    // vendor
    vendor := "go-test"
    vendorLen := len(vendor)
    if vendorLen < 0 || vendorLen > 0xFFFFFFFF {
        vendorLen = 0
    }
    _ = binary.Write(&b, binary.LittleEndian, safeUint32(vendorLen))
    b.WriteString(vendor)
    // list length: 1 comment
    _ = binary.Write(&b, binary.LittleEndian, uint32(1))
    comment := "TITLE=" + title
    commentLen := len(comment)
    if commentLen < 0 || commentLen > 0xFFFFFFFF {
        commentLen = 0
    }
    _ = binary.Write(&b, binary.LittleEndian, safeUint32(commentLen))
    b.WriteString(comment)
    return b.Bytes()
}

func safeUint32(val int) uint32 {
    if val < 0 || val > 0xFFFFFFFF {
        return 0
    }
    return uint32(val)
}

// helper to wrap a packet into a minimal Ogg page (single segment)
func makeOggPage(packet []byte, granule uint64) []byte {
    var p bytes.Buffer
    p.WriteString("OggS")
    p.WriteByte(0) // version
    p.WriteByte(0) // header_type
    gp := make([]byte, 8)
    binary.LittleEndian.PutUint64(gp, granule)
    p.Write(gp)
    p.Write([]byte{0, 0, 0, 1}) // serial
    p.Write([]byte{0, 0, 0, 1}) // seq
    p.Write([]byte{0, 0, 0, 0}) // checksum
    p.WriteByte(1)              // page_segments
    p.WriteByte(byte(len(packet)))
    p.Write(packet)
    return p.Bytes()
}

func TestStreamer_DetectsTitleChanges(t *testing.T) {
    // Build two pages with different TITLE values
    p1 := makeOpusTagsPacket("First Title")
    p2 := makeOpusTagsPacket("Second Title")
    page1 := makeOggPage(p1, 0)
    page2 := makeOggPage(p2, 48000)

    // Concatenate and create reader
    r := bytes.NewReader(append(page1, page2...))

    // Create fake client
    sc := NewStreamingClient(ClientConfig{ServerURL: "", Username: "t", Channel: "c", Bitrate: 64000, Channels: 2})
    f := &fakeStreamClient{StreamingClient: sc}

    // Run the internal demuxer directly (simulating the streamer's demux path)
    d := oggdemux.New(r)
    var last string
    for {
        page, err := d.NextPage()
        if err == io.EOF {
            break
        }
        if err != nil {
            t.Fatalf("demux error: %v", err)
        }
        for _, pkt := range page.Packets {
            if pkt.IsVorbisComment {
                // simulate streamer behavior: extract TITLE and send meta when changed
                title := ""
                // naive parse: look for TITLE= in the packet (we could reuse parseOpusTagsTitle but keep test independent)
                s := string(pkt.Data)
                idx := strings.Index(strings.ToUpper(s), "TITLE=")
                if idx >= 0 {
                    title = s[idx+6:]
                } else {
                    title = s
                }
                if title != last {
                    last = title
                    _ = f.SendMeta(context.Background(), title)
                }
            }
        }
    }

    if len(f.sent) < 2 {
        t.Fatalf("expected at least 2 meta sends, got %d: %v", len(f.sent), f.sent)
    }
    if f.sent[0] == f.sent[1] {
        t.Fatalf("expected different titles sent, got same: %v", f.sent)
    }
}
