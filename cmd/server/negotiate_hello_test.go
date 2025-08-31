package main

import (
    "testing"
    "time"

    "whotalkie/pkg/protocol"
    "whotalkie/internal/state"
    "whotalkie/internal/types"
)

func makeBaseUser(id string) *types.User {
    return &types.User{
        ID: id,
        Username: "old",
        Capabilities: types.DefaultWebClientCapabilities(),
    }
}

func TestNegotiateHelloTable(t *testing.T) {
    cases := []struct{
        name string
        data map[string]interface{}
        wantAccepted bool
        wantCode string
    }{
        {name: "valid hello", data: map[string]interface{}{"username":"alice","channel":"room1","clientType":"web"}, wantAccepted:true, wantCode:""},
        {name: "invalid username", data: map[string]interface{}{"username":""}, wantAccepted:true, wantCode:""}, // empty username is treated as no-op
    {name: "bad username chars", data: map[string]interface{}{"username":"<script>"}, wantAccepted:false, wantCode: protocol.CodeInvalidUsername},
    {name: "invalid channel", data: map[string]interface{}{"channel":"..\n"}, wantAccepted:false, wantCode: protocol.CodeInvalidChannel},
    {name: "bad transmit format", data: map[string]interface{}{"clientType":"custom","capabilities": map[string]interface{}{"transmit_format": map[string]interface{}{"codec":"nope","bitrate":9999999,"sample_rate":123,"channels":8}}}, wantAccepted:false, wantCode: protocol.CodeUnsupportedCodec},
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            sm := state.NewManager()
            user := makeBaseUser("u1")
            ws := &types.WebSocketConnection{UserID: user.ID, Send: make(chan []byte, 4)}
            sm.AddUser(user)
            sm.AddClient(user.ID, ws)

            cm := &ConnectionManager{wsConn: ws, user: user, stateManager: sm, userID: user.ID}

            ev := &types.PTTEvent{Type:"hello", UserID:user.ID, Timestamp:time.Now(), Data: tc.data}
            res := cm.negotiateHello(ev)

            if res.Accepted != tc.wantAccepted {
                t.Fatalf("case %s: accepted got %v want %v", tc.name, res.Accepted, tc.wantAccepted)
            }
            if !tc.wantAccepted {
                // expect structured extras.code
                if res.Extras == nil {
                    t.Fatalf("case %s: expected extras with code, got nil", tc.name)
                }
                code, _ := res.Extras["code"].(string)
                if code != tc.wantCode {
                    t.Fatalf("case %s: expected code %s got %s", tc.name, tc.wantCode, code)
                }
            }
        })
    }
}

func TestNegotiateHelloNormalization(t *testing.T) {
    sm := state.NewManager()
    user := &types.User{ID: "u-norm", Username: "u", Capabilities: types.DefaultWebClientCapabilities(), IsWebClient: false}
    ws := &types.WebSocketConnection{UserID: user.ID, Send: make(chan []byte, 4)}
    sm.AddUser(user)
    sm.AddClient(user.ID, ws)

    cm := &ConnectionManager{wsConn: ws, user: user, stateManager: sm, userID: user.ID}

    // Provide a transmit format that needs clamping: bitrate too high, channels=8
    caps := map[string]interface{}{"clientType":"custom","capabilities": map[string]interface{}{"transmit_format": map[string]interface{}{"codec":"opus","bitrate":500000,"sample_rate":12345,"channels":8}}}
    ev := &types.PTTEvent{Type:"hello", UserID:user.ID, Timestamp: time.Now(), Data: caps}

    res := cm.negotiateHello(ev)
    if !res.Accepted {
        t.Fatalf("expected accepted negotiation, got rejected: %v", res.Extras)
    }
    if res.UpdatedUser == nil {
        t.Fatalf("expected updated user")
    }
    tf := res.UpdatedUser.Capabilities.TransmitFormat
    if tf.Bitrate != 320000 {
        t.Fatalf("expected bitrate clamped to 320000, got %d", tf.Bitrate)
    }
    if tf.Channels != 2 {
        t.Fatalf("expected channels clamped to 2, got %d", tf.Channels)
    }
    if tf.SampleRate != 48000 {
        t.Fatalf("expected sample rate normalized to 48000, got %d", tf.SampleRate)
    }
    if res.Extras == nil {
        t.Fatalf("expected extras describing adjustments")
    }
}
