package oggdemux

// DemuxerAPI defines the minimal demuxer interface used by higher-level
// components. Keeping an explicit interface here makes it easier to swap in
// a cgo-backed implementation or a mocked demuxer in tests.
type DemuxerAPI interface {
    // NextPage returns the next parsed Page or an error (io.EOF when stream ends).
    NextPage() (*Page, error)

    // PacketTimestampUS returns a best-effort timestamp in microseconds for a packet.
    PacketTimestampUS(pkt *Packet) uint64
}
