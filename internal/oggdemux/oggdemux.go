// Package oggdemux provides a thin abstraction for demuxing Ogg/Opus streams.
//
// This is a scaffolded implementation: the core parsing logic is not implemented
// yet. The API is designed so real demuxing can be added later or backed by
// a cgo wrapper for libogg.
package oggdemux

import (
	"bufio"
	"encoding/binary"
	"io"
)

// Packet represents a logical packet extracted from an Ogg page.
// Data contains the raw packet bytes (for Opus packets this is the Opus payload).
// Flags indicate whether the packet is a Vorbis comment or an Opus packet.
type Packet struct {
	Data             []byte
	IsVorbisComment  bool
	IsOpus           bool
	Channels         int // number of channels (1 or 2) when known
	GranulePos       uint64 // granule position of the page this packet came from (best-effort)
}

// Page represents an Ogg page containing one or more logical packets.
type Page struct {
	Packets []Packet
	GranulePosition uint64
}

// Demuxer reads from an io.Reader and exposes Ogg pages and packets.
// Currently this is a stub that returns io.EOF immediately. Replace with
// real parsing logic or a cgo-backed implementation.
type Demuxer struct {
	r *bufio.Reader
	// last known channel count (updated from OpusHead packet)
	lastChannels int
	// partial holds an incomplete logical packet that continues on the next page
	partial       []byte
	// partialContinued indicates whether partial was created by a page that ended
	// with a continued packet (i.e. last segment length == 255)
	partialContinued bool
}

// New creates a Demuxer that will read Ogg data from r.
func New(r io.Reader) *Demuxer {
	return &Demuxer{r: bufio.NewReader(r)}
}

// NextPage returns the next Page from the stream. The scaffold returns io.EOF
// immediately. Implement Ogg page parsing here.
func (d *Demuxer) NextPage() (*Page, error) {
	// Read header (sync + remaining header bytes)
	header, err := d.readHeader()
	if err != nil {
		return nil, err
	}

	gp := binary.LittleEndian.Uint64(header[6:14])
	pageSegments := uint8(header[26])
	headerType := header[5]

	segTable, payload, err := d.readSegmentTableAndPayload(pageSegments)
	if err != nil {
		return nil, err
	}

	packets := d.assemblePackets(segTable, payload, gp, headerType)

	return &Page{Packets: packets, GranulePosition: gp}, nil
}

// readHeader finds the OggS sync pattern and returns the full 27-byte header.
func (d *Demuxer) readHeader() ([]byte, error) {
	header := make([]byte, 27)
	var err error
	var b byte
	// prime first 4 bytes
	if _, err = io.ReadFull(d.r, header[0:4]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, io.EOF
		}
		return nil, err
	}
	// slide until we find sync
	for string(header[0:4]) != "OggS" {
		if b, err = d.r.ReadByte(); err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, err
		}
		header[0] = header[1]
		header[1] = header[2]
		header[2] = header[3]
		header[3] = b
	}
	if _, err = io.ReadFull(d.r, header[4:27]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, io.EOF
		}
		return nil, err
	}
	return header, nil
}

// readSegmentTableAndPayload reads the segment table and the payload bytes
// described by the table. Returns segTable and payload slices.
func (d *Demuxer) readSegmentTableAndPayload(pageSegments uint8) ([]byte, []byte, error) {
	segTable := make([]byte, pageSegments)
	if _, err := io.ReadFull(d.r, segTable); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, nil, io.EOF
		}
		return nil, nil, err
	}
	var total int
	for _, v := range segTable {
		total += int(v)
	}
	payload := make([]byte, total)
	if total > 0 {
		if _, err := io.ReadFull(d.r, payload); err != nil {
			if err == io.ErrUnexpectedEOF {
				return nil, nil, io.EOF
			}
			return nil, nil, err
		}
	}
	return segTable, payload, nil
}

// assemblePackets converts segments+payload into logical Packets, handling
// continued packets using d.partial and updating d.lastChannels as needed.
func (d *Demuxer) assemblePackets(segTable []byte, payload []byte, gp uint64, headerType byte) []Packet {
	var packets []Packet
	var cur []byte
	idx := 0

	isContinuation := (headerType & 0x01) != 0
	if isContinuation && len(d.partial) > 0 {
		cur = append(cur, d.partial...)
		d.partial = nil
		d.partialContinued = false
	}

	for i := 0; i < len(segTable); i++ {
		segLen := int(segTable[i])
		if segLen > 0 {
			cur = append(cur, payload[idx:idx+segLen]...)
			idx += segLen
		}
		if segLen < 255 {
			pkt := d.makePacket(cur, gp)
			packets = append(packets, pkt)
			cur = cur[:0]
		}
	}
	if len(cur) > 0 {
		d.partial = append([]byte(nil), cur...)
		d.partialContinued = true
	}
	return packets
}

// makePacket constructs a Packet from raw bytes and updates demuxer state
// (e.g. lastChannels) when headers like OpusHead are encountered.
func (d *Demuxer) makePacket(cur []byte, gp uint64) Packet {
	pkt := Packet{Data: append([]byte(nil), cur...), GranulePos: gp}
	if len(pkt.Data) >= 8 && string(pkt.Data[0:8]) == "OpusTags" {
		pkt.IsVorbisComment = true
	}
	if len(pkt.Data) >= 9 && string(pkt.Data[0:8]) == "OpusHead" {
		ch := int(pkt.Data[9])
		if ch > 0 {
			d.lastChannels = ch
		}
	}
	if !pkt.IsVorbisComment && (len(pkt.Data) < 8 || string(pkt.Data[0:8]) != "OpusHead") {
		pkt.IsOpus = true
	}
	if d.lastChannels > 0 {
		pkt.Channels = d.lastChannels
	} else {
		pkt.Channels = 1
	}
	return pkt
}

// PacketTimestampUS returns the best-effort timestamp for a packet in
// microseconds. This is a placeholder for future implementations that
// compute timestamps from granule positions or publisher-supplied timing.
func (d *Demuxer) PacketTimestampUS(pkt *Packet) uint64 {
	// Best-effort: use packet's granule position as sample position (samples)
	// and convert to microseconds assuming 48kHz sample rate (Opus default)
	if pkt == nil {
		return 0
	}
	if pkt.GranulePos == 0 {
		return 0
	}
	// granule position counts PCM samples (per channel) of the last sample
	// Convert to microseconds: samples * 1e6 / 48000
	return (pkt.GranulePos * 1000000) / 48000
}
