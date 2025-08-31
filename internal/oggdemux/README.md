This package `internal/oggdemux` is a scaffold for Ogg demuxing.

Planned responsibilities:
- Parse Ogg pages and expose logical packets (Opus payloads and Vorbis comments).
- Provide packet timestamp extraction (from granule positions).
- Allow a streaming mode where raw Ogg bytes are read and Opus packets are emitted as they arrive.

Implementation notes:
- Prefer a pure-Go demuxer if a maintained library exists.
- If no suitable pure-Go library is available, implement a small cgo wrapper around libogg to parse pages and extract packets. Keep the Go API minimal and test-driven.

Tests currently assert EOF behavior; implement real parsing and expand tests with sample Ogg files.
