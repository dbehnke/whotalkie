# Architecture Overview

WhoTalkie is a lightweight PTT streaming system. High-level components:

- Server (`cmd/server`): manages channels, relays framed Opus payloads and metadata via WebSocket, handles client registry and broadcast loops.
- Streamer client (`cmd/whotalkie-stream`): publish-only clients demux container formats and send framed Opus binary and `meta` JSON messages.
- Web UI (`web/`): browser client using WebCodecs & WebAudio for low-latency playback.
- Internal packages: `internal/state` (manager), `internal/oggdemux` (demux helpers), `pkg/client` (client helpers).

Design principles:
- Keep server stateless where possible; push demux responsibilities to clients to avoid server CPU/cgo needs.
- Bounded worker pools and non-blocking enqueue strategies protect the server from bursty metadata floods.
- Graceful shutdown ordering ensures no workers write to closed channels.
