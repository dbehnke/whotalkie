# WhoTalkie Opus Transport & Ogg Demux Spec

This document describes the designed transport for WhoTalkie:
- Interactive PTT: framed raw Opus packets over WebSocket (low-latency).
- Publish-only streams: accept Ogg/Opus input, demux into raw Opus frames + metadata, broadcast frames to WebCodecs-capable web clients, and proxy original Ogg bytes for native/non-web listeners.

Keep server pass-through: no server-side re-encoding.

---

## Requirements checklist

- [x] Interactive PTT uses framed raw Opus packets over WebSocket (mono, 32kbps).
- [x] Publish-only sources may provide Ogg/Opus. Server demuxes Ogg into Opus packets and Vorbis comment metadata.
- [x] Server broadcasts framed Opus binary messages to subscribers who prefer `opus-raw` or `webcodecs`.
- [x] Server exposes `GET /stream/:channel.ogg` HTTP endpoint that proxies the original Ogg bytes (preserving Vorbis comments/metadata) for native players.
- [x] Clients perform capability negotiation at WebSocket connect; server routes accordingly.

---

## Protocol overview

1. Client connects over WebSocket to `/ws`.
2. Client sends a JSON `hello` with capabilities.
3. Server responds with acceptance and any current `meta` for the joined channel.
4. Publishers (publish-only) may send Ogg bytes (container) as their input. Server demuxes Ogg and emits:
   - JSON `meta` messages when Vorbis comments / ICY metadata change.
   - Binary framed Opus packets to subscribers.
   - Keeps original Ogg stream available via HTTP for non-web clients.
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

## Server-side Ogg demux pseudo-code (high-level)

Goal: accept an Ogg/Opus byte stream from a publish-only source, extract Vorbis comments (metadata) and Opus packet payloads, and broadcast frames to subscribers.

Notes: This is pseudo-code — adapt to your actual Go server architecture and concurrency model.

```go
// Accept a connection from a publisher (streamer)
func handlePublisher(conn net.Conn, channel string) {
    // 1) keep raw Ogg stream available for HTTP proxy
    oggReader := bufio.NewReader(conn)
    // Start goroutine to copy raw bytes into a ring buffer used by HTTP proxy
    go copyRawToRing(channelRawBuffer(channel), oggReader)

    // 2) initialize an Ogg demuxer on the same byte stream
    demux := oggdemux.New(oggReader)

    var lastTags map[string]string
    seq := uint32(0)

    for {
        page, err := demux.NextPage()
        if err == io.EOF { break }
        if err != nil { log.Println("demux error", err); break }

        // demux returns logical packets (Opus packets or comment/header packets)
        for _, pkt := range page.Packets {
            if pkt.IsVorbisComment {
                tags := parseVorbisComment(pkt.Data)
                if !equal(tags, lastTags) {
                    lastTags = tags
                    broadcastJSONToSubscribers(channel, metaMessage(channel, tags))
                }
                continue
            }

            if pkt.IsOpus {
                // create framed binary message
                tsUs := demux.PacketTimestampUS(pkt)
                frame := frameHeader(seq, tsUs, pkt.Channels, len(pkt.Data))
                frame = append(frame, pkt.Data...)
                broadcastBinaryToSubscribers(channel, frame)
                seq++
            }
        }
    }

    // publisher ended
    broadcastJSONToSubscribers(channel, publisherStopMessage(channel))
}
```

Implementation notes:
- `oggdemux` is a conceptual demuxer that exposes Ogg pages and logical packets.
- Use an efficient ring buffer for the raw Ogg HTTP proxy to avoid blocking the publisher.
- Consider backpressure: if HTTP clients are slow, drop or disconnect them rather than blocking the publisher.

---

## Recommended Go library options / approaches

Two main approaches to implement Ogg demuxing in Go:

A) Integrate a pure-Go Ogg demuxer/demultiplexer (preferred if it exists and meets needs):
- Advantages: easier deployment (no cgo), Go-level concurrency, natural integration.
- Candidate ideas: look for active projects providing Ogg/Opus demuxing. If a maintained project is available, prefer that.

B) Use libogg/libopus via cgo or a small C wrapper:
- Advantages: mature, tested demuxing in libogg; more reliable handling of edge cases in streamed Ogg.
- Use cgo to call libogg APIs to parse pages and extract packets, then pass packet bytes to Go runtime.
- Tradeoff: introduces cgo, build complexity.

Opus packet utilities in Go:
- `github.com/hraban/opus` — Go bindings for libopus (useful for optional packet introspection).
- `github.com/pion/rtp` — useful if you want RTP fallback or packet structures (not required for raw ws framing).

Notes: evaluate candidate libraries for maintenance status and compatibility with your Go version. If no trustworthy pure-Go demuxer is available, prefer cgo + libogg, then wrap into an internal package (e.g. `internal/oggdemux`).

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
