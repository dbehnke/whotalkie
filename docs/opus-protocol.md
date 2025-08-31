# WhoTalkie Opus Transport & Ogg Demux Spec

This document describes the designed transport for WhoTalkie.

High-level design change (server responsibility):
- The server is responsible only for proxying and relaying framed raw Opus packets and metadata messages between clients. It does not perform container demuxing in the common case.
- Publish-only streamers (clients) are responsible for demuxing Ogg/Opus (or extracting Opus frames from other containers) and sending raw Opus frames and any associated metadata (e.g. song title, artist) to the server.

Keep server pass-through: no server-side re-encoding. The server should only forward raw Opus bytes and compact JSON metadata messages.

---

## Requirements checklist

- [x] Interactive PTT uses framed raw Opus packets over WebSocket (mono, 32kbps).
- [x] Publish-only streamers demux any container formats (Ogg/Opus, WebM, etc.) locally and send raw Opus frames and metadata to the server; the server does not demux by default.
- [x] Server broadcasts framed Opus binary messages to subscribers who prefer `webcodecs` or `opus-raw`.
- [ ] (Optional) Server may provide HTTP proxy endpoints for raw container bytes in specific deployments; this is not required for the core design.
- [x] Clients perform capability negotiation at WebSocket connect; server routes accordingly.

---

## Protocol overview

1. Client connects over WebSocket to `/ws`.
2. Client sends a JSON `hello` with capabilities.
3. Server responds with acceptance and any current `meta` for the joined channel.
4. Publishers (publish-only) are expected to demux any containers locally and send the server:
  - JSON `meta` messages when metadata changes (title, artist, tags). Metadata messages are small UTF-8 JSON frames.
  - Binary framed raw Opus packets (using the framing spec below) for audio payloads.
  The server simply relays these messages to subscribers; it does not need to inspect or parse container formats.
5. Interactive PTT clients send framed Opus packets directly (no container).

---

## WebSocket JSON control messages

All JSON control messages are UTF-8 text WebSocket messages.

- Client -> Server: `hello`

```json
{
  "type": "hello",
  "clientType": "web" | "native" | "streamer",
  "supports": ["webcodecs","ogg-http","opus-raw"],
  "preferred": "webcodecs" | "ogg-http" | "opus-raw",
  "ptt": true | false,
  "bitrate": 32000,
  "channels": 1
}
```

- Server -> Client: `welcome` (ack)

```json
{
  "type": "welcome",
  "channel": "music",
  "accepted": true,
  "sessionId": "abc123"
}
```

- Server -> Clients: `meta` (metadata update)

```json
{
  "type": "meta",
  "channel": "music",
  "source": "LiveRadio",
  "tags": { "title": "Song Name", "artist": "Artist Name" },
  "bitrate": 64000,
  "channels": 2,
  "timestamp_us": 169... 
}
```

- Server -> Clients: `publisher` (state)

```json
{ "type": "publisher", "channel": "music", "action": "start" }
{ "type": "publisher", "channel": "music", "action": "stop" }
```

Notes: JSON messages should be small and infrequent. Use binary messages for high-volume audio.

---

## Binary framing format for Opus packets

Binary WebSocket messages contain a small header followed by the raw Opus packet bytes (no additional encapsulation). All integers are big-endian.

Header (fixed-length):
- 4 bytes: sequence number (uint32)
- 8 bytes: timestamp_us (uint64) — publisher-provided or server monotonic microseconds
- 1 byte : channels (uint8)
- 2 bytes: payload_len (uint16)

Then: `payload_len` bytes of the exact Opus packet payload as produced by libopus/ffmpeg.

Total header size: 15 bytes.

Rationale:
- Sequence+timestamp allow ordering and jitter-buffering on clients before feeding WebCodecs.
- Channels lets clients decide if they need to downmix (clients should support channel count announced in `meta`/`hello`).
- Server relays payloads exactly as received — no re-encoding.

Example pseudo-serialization (Go):

```go
// header -> [4 bytes seq][8 bytes ts_us][1 byte channels][2 bytes len]
buf := make([]byte, 15+len(payload))
binary.BigEndian.PutUint32(buf[0:], seq)
binary.BigEndian.PutUint64(buf[4:], tsUs)
buf[12] = byte(channels)
binary.BigEndian.PutUint16(buf[13:], uint16(len(payload)))
copy(buf[15:], payload)
// send buf as a binary ws message
```

Clients should accept frames with variable `payload_len` and use timestamp/seq to de-jitter.

---

## Publisher responsibilities (demux on the client)

Publishers that ingest container formats (Ogg/Opus, WebM, etc.) should:

- Demux the container locally and extract raw Opus packets and metadata.
- Send compact JSON `meta` messages to the server when metadata (Vorbis comments, ICY tags, etc.) changes.
- Send framed binary raw Opus packets to the server using the agreed framing format (below).
- Optionally keep a local copy or HTTP proxy of the original container if you need to support native players — the server does not require this.

Rationale: pushing demuxing to clients simplifies the server, reduces server CPU and complexity, and avoids introducing cgo requirements into the server build.

Note on backpressure and silence: clients SHOULD avoid sending audio frames during extended silence; the server will not transmit audio data when none is received for a channel. Metadata messages should still be sent when relevant.

---

## Recommended approach

Because the server no longer needs to demux containers by default, there are two practical choices:

1. Keep the current `internal/oggdemux` scaffold for in-repo testing and optional server-side demuxing in special deployments.
2. Prefer implementing demuxing in publish-only clients (streamers). For client-side implementations you can choose a pure-Go demuxer (no cgo) or use libogg via cgo depending on requirements and platform constraints.

Opus packet utilities in Go (useful for client-side tooling):
- `github.com/hraban/opus` — Go bindings for libopus (optional packet introspection).
- `github.com/pion/rtp` — useful if you want RTP fallback or packet structures (not required for raw ws framing).

If you later decide to support server-side container proxy endpoints (e.g., `GET /stream/:channel.ogg`), you can provide them as optional deployment features that are populated by client upload or server-side demuxing helpers.

---

## Client-side WebCodecs consumption snippet (browser JS)

This snippet shows how a web client can receive the binary framed messages and feed them into WebCodecs AudioDecoder. It assumes the browser supports WebCodecs for Opus.

```js
// example: websocket is an open WebSocket
// audioCtx used for playback via AudioWorklet or AudioDecoder -> AudioBufferSource
const audioDecoder = new AudioDecoder({
  output: (pcm) => {
    // pcm is AudioData — connect to an AudioWorklet or AudioContext
    // ...application-specific playback...
  },
  error: (e) => console.error('decoder error', e)
});

// configure decoder for Opus
audioDecoder.configure({ codec: 'opus', sampleRate: 48000, numberOfChannels: 2 });

let lastSeq = 0;

ws.onmessage = async (event) => {
  if (typeof event.data === 'string') {
    const msg = JSON.parse(event.data);
    if (msg.type === 'meta') {
      // update UI with song/title
      updateSongMetadata(msg.tags);
    }
    return;
  }

  // binary frame
  const data = new Uint8Array(await event.data.arrayBuffer());
  const seq = (data[0]<<24)|(data[1]<<16)|(data[2]<<8)|data[3];
  const tsUs = (
    BigInt(data[4])<<56n | BigInt(data[5])<<48n | BigInt(data[6])<<40n | BigInt(data[7])<<32n |
    BigInt(data[8])<<24n | BigInt(data[9])<<16n | BigInt(data[10])<<8n | BigInt(data[11])
  );
  const channels = data[12];
  const payloadLen = (data[13]<<8)|data[14];
  const payload = data.subarray(15, 15 + payloadLen);

  // construct EncodedAudioChunk
  const timestamp = Number(tsUs / 1000n); // convert us -> ms if desired; WebCodecs uses microseconds in some APIs — verify in runtime
  const chunk = new EncodedAudioChunk({
    type: 'key', // Opus frames are typically independent enough for decoding
    timestamp: Number(tsUs),
    data: payload
  });

  // enqueue
  try {
    audioDecoder.decode(chunk);
  } catch (e) {
    console.error('decode failed', e);
  }
};
```

Notes about timestamps and units: choose a consistent timestamp unit (we defined `timestamp_us` in microseconds). When creating an EncodedAudioChunk, the browser API expects a timestamp in microseconds (check current spec) — ensure units match.

Also ensure proper jitter-buffering: collect a few packets before decoding to absorb network jitter. Sequence numbers let you reorder or detect missing packets.

---

## Testing & verification

- Unit tests for `internal/oggdemux` reading sample Ogg files and asserting tag extraction + correct packet byte output.
- Integration test: start server locally, `ffmpeg ... -f ogg -c:a libopus ... | ./whotalkie-stream -stdin -container ogg`, open a headless Chromium test that connects and verifies it receives `meta` updates and playable audio frames.
- Smoke test: `ffplay http://localhost:8080/stream/music.ogg` should play the raw Ogg stream with metadata.

---

## Next steps / recommended implementation order

1. Add `internal/oggdemux` package (select approach: pure-Go or cgo+libogg) and unit tests.
2. Implement WS `hello` capability handshake and routing in `cmd/server/main.go`.
3. Implement publisher handling and the demux loop; broadcast binary frames + `meta` messages.
4. Add HTTP proxy endpoint `GET /stream/:channel.ogg` which reads from the publisher raw ring buffer.
5. Update `web/static/dashboard.js` to perform handshake, handle `meta`, and feed binary frames into WebCodecs.
6. Add integration tests.

---

If you'd like, I can now:
- produce the concrete WebSocket + framing spec as a separate compact doc (diff-ready),
- scaffold `internal/oggdemux` with a demux interface and unit test stubs, or
- implement the client-side changes to `web/static/dashboard.js`.

Tell me which to do next.
