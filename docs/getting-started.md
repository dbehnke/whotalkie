# Getting Started

Quick steps to run WhoTalkie locally for development:

1. Install Go and ensure `go` is on your PATH.
2. Build server and client:

```bash
go build ./cmd/server
go build ./cmd/whotalkie-stream
```

3. Run server locally:

```bash
./server -port 8080
```

4. Use the `whotalkie-stream` example or `ffmpeg` to stream into the server.

See `docs/ops.md` for deployment notes.
