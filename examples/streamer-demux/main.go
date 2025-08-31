package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"whotalkie/pkg/client"
)

// This example demonstrates how a publish-only streamer could demux a local
// Ogg/Opus source and send metadata + raw Opus frames to the server. It is
// illustrative: replace `demuxAndStream` with a real demuxing implementation
// or library in a production client.

func main() {
	ctx := context.Background()

	cfg := client.ClientConfig{
		ServerURL: "ws://localhost:8080/ws",
		Username:  "ExampleStreamer",
		Channel:   "music",
		Bitrate:   64000,
		Channels:  2,
		PublishOnly: true,
	}

	sc := client.NewStreamClient(cfg)
	if err := sc.Connect(ctx); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer sc.Disconnect()

	if err := sc.NegotiateCapabilities(ctx); err != nil {
		log.Fatalf("negotiate: %v", err)
	}

	if err := sc.JoinChannel(ctx); err != nil {
		log.Fatalf("join: %v", err)
	}

	// In a real app, open an Ogg/Opus source (stdin, file, or network) and demux.
	// Here we'll just show a placeholder that reads from a file and calls the
	// send functions. Replace demuxAndStream with a real demuxer.

	f, err := os.Open("sample.opus.ogg")
	if err != nil {
		fmt.Println("sample file not found; example will exit")
		return
	}
	defer f.Close()

	err = demuxAndStream(ctx, sc, f)
	if err != nil && err != io.EOF {
		log.Printf("demux/stream error: %v", err)
	}

	// signal stop
	sc.StopTransmission(ctx)
}

// demuxAndStream is a placeholder. Replace this with actual demuxing logic.
// It should call sc.SendAudioData for each raw Opus packet and sc.SendEvent
// (via the client's API) for metadata changes (song title, artist, etc.).
func demuxAndStream(ctx context.Context, sc *client.StreamClient, r io.Reader) error {
	// NOTE: This example does NOT parse Ogg. It's here to show how a real
	// streamer would call into the client library to send audio and metadata.

	// Example: advertise format and send one small audio payload.
	sc.SetEventHandler(&client.DefaultEventHandler{})
	// In a real streamer you'd send metadata updates as JSON events (meta)
	// via the client's JSON event API. For this minimal example we send a
	// single small audio packet to illustrate usage of SendAudioData.
	_ = sc.SendAudioData(ctx, []byte{0x00})

	// In practice you'd loop over demuxed Opus packets, calling sc.SendAudioData
	// with the raw packet bytes. For silence, simply don't send audio frames.
	return io.EOF
}
