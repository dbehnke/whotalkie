This package `internal/oggdemux` is a scaffold for Ogg demuxing and is optional.

Design note: the server is intended to proxy raw Opus packets and metadata; demuxing container formats is the responsibility of publish-only streamers (clients). This package may be used for in-repo testing or optional server-side demuxing in special deployments.

Planned responsibilities (optional server-side use):
- Parse Ogg pages and expose logical packets (Opus payloads and Vorbis comments).
- Provide packet timestamp extraction (from granule positions).
- Allow a streaming mode where raw Ogg bytes are read and Opus packets are emitted as they arrive.

Implementation notes:
- Prefer a pure-Go demuxer if a maintained library exists.
- If no suitable pure-Go library is available, implement a small cgo wrapper around libogg to parse pages and extract packets. Keep the Go API minimal and test-driven.

Tests currently assert EOF behavior; implement real parsing and expand tests with sample Ogg files if the server-side demux option is needed.

See `examples/streamer-demux` for a minimal client-side example showing how a streamer would demux locally and send metadata + raw Opus frames to the server.
