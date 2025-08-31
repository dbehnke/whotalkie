package oggdemux

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestDemuxer_NextPage_EOF(t *testing.T) {
	r := bytes.NewReader([]byte{})
	d := New(r)
	p, err := d.NextPage()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v (page=%v)", err, p)
	}
}

func TestPacketTimestampUS_DefaultZero(t *testing.T) {
	r := bytes.NewReader([]byte{})
	d := New(r)
	pkt := &Packet{Data: []byte{0x01, 0x02}, IsOpus: true}
	if ts := d.PacketTimestampUS(pkt); ts != 0 {
		t.Fatalf("expected default timestamp 0, got %d", ts)
	}
}

// A minimal smoke test to ensure New accepts any io.Reader and NextPage
// returns EOF on empty input.
func TestDemuxer_EmptyStream(t *testing.T) {
	r := bytes.NewReader([]byte{})
	d := New(r)
	_, err := d.NextPage()
	if err != io.EOF {
		t.Fatalf("expected EOF on empty stream, got %v", err)
	}
}

// Test that the package compiles with a non-empty reader by driving
// New with a reader that returns data slowly.
func TestDemuxer_WithSlowReader(t *testing.T) {
	r := &slowReader{data: []byte{0x00, 0x01, 0x02}}
	d := New(r)
	_, err := d.NextPage()
	if err != io.EOF {
		t.Fatalf("expected EOF for scaffold demuxer, got %v", err)
	}
}

// Test parsing of a minimal Ogg page containing a single OpusTags packet.
func TestDemuxer_ParseMinimalOpusTagsPage(t *testing.T) {
	// Build a simple Ogg page:
	// capture pattern 'OggS' + version(0) + header_type(0) + granulepos (8) little endian
	// + serial (4) + seq (4) + checksum (4) + page_segments(1) + seg_table(1)
	// payload: "OpusTags" + minimal payload

	gp := uint64(48000) // 1 second
	page := make([]byte, 0)
	// header
	page = append(page, []byte("OggS")...)
	page = append(page, 0) // version
	page = append(page, 0) // header_type
	// granule pos little-endian
	gpbuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(gpbuf, gp)
	page = append(page, gpbuf...)
	// serial
	page = append(page, []byte{0, 0, 0, 1}...)
	// seq
	page = append(page, []byte{0, 0, 0, 1}...)
	// checksum
	page = append(page, []byte{0, 0, 0, 0}...)
	// one segment
	page = append(page, 1)
	// segment table: length of payload (we'll use 9 bytes)
	page = append(page, 9)
	// payload: "OpusTags" + a trailing 0 byte
	page = append(page, []byte("OpusTags")...)
	page = append(page, 0)

	r := bytes.NewReader(page)
	d := New(r)
	p, err := d.NextPage()
	if err != nil {
		t.Fatalf("unexpected error from NextPage: %v", err)
	}
	if len(p.Packets) != 1 {
		t.Fatalf("expected 1 packet, got %d", len(p.Packets))
	}
	pkt := p.Packets[0]
	if !pkt.IsVorbisComment {
		t.Fatalf("expected IsVorbisComment true, got false")
	}
	if p.GranulePosition != gp {
		t.Fatalf("expected granule %d got %d", gp, p.GranulePosition)
	}
	// timestamp should convert to ~1_000_000 us
	ts := d.PacketTimestampUS(&pkt)
	if ts == 0 {
		t.Fatalf("expected non-zero timestamp from granule, got 0")
	}
}

// Test that a packet split across two pages (continued packet) is reassembled.
func TestDemuxer_ContinuedPacketAcrossPages(t *testing.T) {
	// Build two pages. First page contains a segment with length 255 (continued),
	// second page continues the packet and ends it.
	// We'll craft a payload of 300 bytes and split it 255/45 across two pages.
	part1 := make([]byte, 255)
	for i := range part1 {
		part1[i] = 'A'
	}
	part2 := make([]byte, 45)
	for i := range part2 {
		part2[i] = 'B'
	}
	total := append(part1, part2...)

	// Page 1 header (normal, ends with a segment value 255 -> packet continues)
	p1 := make([]byte, 0)
	p1 = append(p1, []byte("OggS")...)
	p1 = append(p1, 0)
	p1 = append(p1, 0) // header_type: normal
	gp1 := uint64(0)
	g1 := make([]byte, 8)
	binary.LittleEndian.PutUint64(g1, gp1)
	p1 = append(p1, g1...)
	p1 = append(p1, []byte{0, 0, 0, 1}...)
	p1 = append(p1, []byte{0, 0, 0, 1}...)
	p1 = append(p1, []byte{0, 0, 0, 0}...)
	// one segment valued 255
	p1 = append(p1, 1)
	p1 = append(p1, byte(255))
	p1 = append(p1, total[:255]...)

	// Page 2 header (continuation bit set, provides remaining 45 bytes)
	p2 := make([]byte, 0)
	p2 = append(p2, []byte("OggS")...)
	p2 = append(p2, 0)
	p2 = append(p2, 1) // header_type: continued (bit 0 set)
	gp2 := uint64(48000)
	g2 := make([]byte, 8)
	binary.LittleEndian.PutUint64(g2, gp2)
	p2 = append(p2, g2...)
	p2 = append(p2, []byte{0, 0, 0, 1}...)
	p2 = append(p2, []byte{0, 0, 0, 2}...)
	p2 = append(p2, []byte{0, 0, 0, 0}...)
	// one segment: remaining 45 bytes
	p2 = append(p2, 1)
	p2 = append(p2, byte(len(total)-255))
	p2 = append(p2, total[255:]...)

	// concatenate pages into a reader
	r := bytes.NewReader(append(p1, p2...))
	d := New(r)

	// Read first page: should produce one packet only when completed by second page
	_, err := d.NextPage()
	if err != nil {
		t.Fatalf("unexpected error reading first page: %v", err)
	}
	// second page should result in the reassembled packet being returned
	p, err := d.NextPage()
	if err != nil {
		t.Fatalf("unexpected error reading second page: %v", err)
	}
	if len(p.Packets) != 1 {
		t.Fatalf("expected 1 packet after continuation, got %d", len(p.Packets))
	}
	pkt := p.Packets[0]
	if len(pkt.Data) != len(total) {
		t.Fatalf("expected reassembled packet length %d, got %d", len(total), len(pkt.Data))
	}
}

// Test that an OpusHead packet updates the demuxer's channel count and that
// a following Opus packet inherits that channel count.
func TestDemuxer_OpusHeadChannelDetection(t *testing.T) {
	// Build page 1: OpusHead packet (OpusHead + version(1) + channel_count(2))
	p1 := make([]byte, 0)
	p1 = append(p1, []byte("OggS")...)
	p1 = append(p1, 0)
	p1 = append(p1, 0)
	gp1 := uint64(0)
	g1 := make([]byte, 8)
	binary.LittleEndian.PutUint64(g1, gp1)
	p1 = append(p1, g1...)
	p1 = append(p1, []byte{0, 0, 0, 1}...)
	p1 = append(p1, []byte{0, 0, 0, 1}...)
	p1 = append(p1, []byte{0, 0, 0, 0}...)
	// one segment
	p1 = append(p1, 1)
	payload1 := []byte("OpusHead")
	payload1 = append(payload1, 0x01) // version
	payload1 = append(payload1, 0x02) // channel count = 2
	// segment table length
	p1 = append(p1, byte(len(payload1)))
	p1 = append(p1, payload1...)

	// Build page 2: a small Opus audio packet (not OpusHead/OpusTags)
	p2 := make([]byte, 0)
	p2 = append(p2, []byte("OggS")...)
	p2 = append(p2, 0)
	p2 = append(p2, 0)
	gp2 := uint64(48000)
	g2 := make([]byte, 8)
	binary.LittleEndian.PutUint64(g2, gp2)
	p2 = append(p2, g2...)
	p2 = append(p2, []byte{0, 0, 0, 1}...)
	p2 = append(p2, []byte{0, 0, 0, 2}...)
	p2 = append(p2, []byte{0, 0, 0, 0}...)
	// payload: short non-header bytes
	payload2 := []byte{0x10, 0x20, 0x30}
	p2 = append(p2, 1)
	p2 = append(p2, byte(len(payload2)))
	p2 = append(p2, payload2...)

	r := bytes.NewReader(append(p1, p2...))
	d := New(r)

	// Read first page (OpusHead)
	p, err := d.NextPage()
	if err != nil {
		t.Fatalf("unexpected error reading OpusHead page: %v", err)
	}
	if len(p.Packets) != 1 {
		t.Fatalf("expected 1 packet in OpusHead page, got %d", len(p.Packets))
	}
	if p.Packets[0].IsOpus {
		t.Fatalf("OpusHead packet should not be marked IsOpus")
	}

	// Read second page (audio) and assert channel count inherited
	p2res, err := d.NextPage()
	if err != nil {
		t.Fatalf("unexpected error reading audio page: %v", err)
	}
	if len(p2res.Packets) != 1 {
		t.Fatalf("expected 1 packet in audio page, got %d", len(p2res.Packets))
	}
	pkt := p2res.Packets[0]
	if !pkt.IsOpus {
		t.Fatalf("expected audio packet to be IsOpus")
	}
	if pkt.Channels != 2 {
		t.Fatalf("expected channels 2 from OpusHead, got %d", pkt.Channels)
	}
	if p2res.GranulePosition != gp2 {
		t.Fatalf("expected granule %d got %d", gp2, p2res.GranulePosition)
	}
}

// Test assembly of multiple small packets contained in a single page.
func TestDemuxer_MultiPacketSinglePage(t *testing.T) {
	// Build page with two small payloads: "abc" and "defg"
	p := make([]byte, 0)
	p = append(p, []byte("OggS")...)
	p = append(p, 0)
	p = append(p, 0)
	gp := uint64(12345)
	g := make([]byte, 8)
	binary.LittleEndian.PutUint64(g, gp)
	p = append(p, g...)
	p = append(p, []byte{0, 0, 0, 1}...)
	p = append(p, []byte{0, 0, 0, 1}...)
	p = append(p, []byte{0, 0, 0, 0}...)
	// two segments
	payload1 := []byte("abc")
	payload2 := []byte("defg")
	p = append(p, 2)
	p = append(p, byte(len(payload1)))
	p = append(p, byte(len(payload2)))
	p = append(p, payload1...)
	p = append(p, payload2...)

	r := bytes.NewReader(p)
	d := New(r)
	page, err := d.NextPage()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.GranulePosition != gp {
		t.Fatalf("expected granule %d got %d", gp, page.GranulePosition)
	}
	if len(page.Packets) != 2 {
		t.Fatalf("expected 2 packets, got %d", len(page.Packets))
	}
	if string(page.Packets[0].Data) != "abc" {
		t.Fatalf("packet 0 data mismatch: %s", string(page.Packets[0].Data))
	}
	if string(page.Packets[1].Data) != "defg" {
		t.Fatalf("packet 1 data mismatch: %s", string(page.Packets[1].Data))
	}
}

// Test that a packet composed of multiple segments including a 255-length
// segment is reassembled correctly within the same page.
func TestDemuxer_SegmentedPacketAcrossSegments(t *testing.T) {
	// Create three packets in one page: p1 len3, p2 len(255+10)=265, p3 len2
	part1 := make([]byte, 255)
	for i := range part1 {
		part1[i] = 'X'
	}
	part2 := make([]byte, 10)
	for i := range part2 {
		part2[i] = 'Y'
	}
	pkt1 := []byte{'A', 'B', 'C'}
	pkt3 := []byte{'Z', 'Z'}

	payload := make([]byte, 0)
	payload = append(payload, pkt1...)
	payload = append(payload, part1...)
	payload = append(payload, part2...)
	payload = append(payload, pkt3...)

	p := make([]byte, 0)
	p = append(p, []byte("OggS")...)
	p = append(p, 0)
	p = append(p, 0)
	gp := uint64(0)
	g := make([]byte, 8)
	binary.LittleEndian.PutUint64(g, gp)
	p = append(p, g...)
	p = append(p, []byte{0, 0, 0, 1}...)
	p = append(p, []byte{0, 0, 0, 1}...)
	p = append(p, []byte{0, 0, 0, 0}...)
	// four segments: len(pkt1)=3, 255, 10, len(pkt3)=2
	p = append(p, 4)
	p = append(p, byte(len(pkt1)))
	p = append(p, byte(255))
	p = append(p, byte(10))
	p = append(p, byte(len(pkt3)))
	p = append(p, payload...)

	r := bytes.NewReader(p)
	d := New(r)
	page, err := d.NextPage()
	if err != nil {
		t.Fatalf("unexpected error reading segmented page: %v", err)
	}
	if len(page.Packets) != 3 {
		t.Fatalf("expected 3 packets, got %d", len(page.Packets))
	}
	if string(page.Packets[0].Data) != "ABC" {
		t.Fatalf("packet0 mismatch: %s", string(page.Packets[0].Data))
	}
	if len(page.Packets[1].Data) != 255+10 {
		t.Fatalf("packet1 length mismatch: got %d", len(page.Packets[1].Data))
	}
	if string(page.Packets[2].Data) != "ZZ" {
		t.Fatalf("packet2 mismatch: %s", string(page.Packets[2].Data))
	}
}

// slowReader is an io.Reader that returns at most 1 byte per Read call.
type slowReader struct{ data []byte }

func (s *slowReader) Read(p []byte) (int, error) {
	if len(s.data) == 0 {
		return 0, io.EOF
	}
	p[0] = s.data[0]
	s.data = s.data[1:]
	return 1, nil
}
