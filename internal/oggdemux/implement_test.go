package oggdemux

// Compile-time assertion: ensure that *Demuxer implements DemuxerAPI.
var _ DemuxerAPI = (*Demuxer)(nil)
