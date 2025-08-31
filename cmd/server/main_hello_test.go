package main

import (
    "encoding/json"
    "testing"
    "time"
    "strings"

    "whotalkie/pkg/protocol"

    "whotalkie/internal/state"
    "whotalkie/internal/types"
)

func TestHandleHelloUpdatesUserAndSendsWelcome(t *testing.T) {
    sm := state.NewManager()
    userID := "test-hello-1"
    user := &types.User{
        ID: userID,
        Username: "User_old",
        Capabilities: types.DefaultWebClientCapabilities(),
    }
    ws := &types.WebSocketConnection{
        UserID: userID,
        Send:   make(chan []byte, 10),
    }
    sm.AddUser(user)
    sm.AddClient(userID, ws)

    cm := &ConnectionManager{
        wsConn:       ws,
        user:         user,
        stateManager: sm,
        userID:       userID,
    }

    caps := map[string]interface{}{
        "client_type": "web",
        "user_agent":  "test-agent",
        "transmit_format": map[string]interface{}{
            "codec":       "opus",
            "bitrate":     32000,
            "sample_rate": 48000,
            "channels":    1,
        },
        "supports_variable_br": false,
        "supports_stereo":     false,
    }

    ev := &types.PTTEvent{
        Type:      "hello",
        UserID:    userID,
        Timestamp: time.Now(),
        Data: map[string]interface{}{
            "username":     "alice",
            "channel":      "room1",
            "clientType":   "web",
            "capabilities": caps,
        },
    }

    cm.handleHello(ev)

    // verify state updated
    gotUser, ok := sm.GetUser(userID)
    if !ok {
        t.Fatalf("user not found after handleHello")
    }
    if gotUser.Username != "alice" {
        t.Fatalf("expected username alice, got %s", gotUser.Username)
    }
    if gotUser.Channel != "room1" {
        t.Fatalf("expected channel room1, got %s", gotUser.Channel)
    }

    // verify welcome queued
    select {
    case msg := <-ws.Send:
        var we types.PTTEvent
        if err := json.Unmarshal(msg, &we); err != nil {
            t.Fatalf("failed to unmarshal welcome: %v", err)
        }
        if we.Type != "welcome" {
            t.Fatalf("expected welcome type, got %s", we.Type)
        }
        if accepted, ok := we.Data["accepted"].(bool); !ok || !accepted {
            t.Fatalf("expected accepted=true in welcome, got %v", we.Data["accepted"])
        }
    // verify negotiated_transmit_format present
    ntf, ok := we.Data[protocol.WelcomeKeyNegotiatedTransmitFormat]
        if !ok {
            t.Fatalf("expected negotiated_transmit_format in welcome")
        }
        // Check minimal fields
        if m, ok := ntf.(map[string]interface{}); ok {
            if codec, ok := m["codec"].(string); !ok || strings.ToLower(codec) != "opus" {
                t.Fatalf("expected negotiated codec opus, got %v", m["codec"])
            }
        } else {
            t.Fatalf("negotiated_transmit_format has unexpected type %T", ntf)
        }
    case <-time.After(1 * time.Second):
        t.Fatalf("no message sent to client Send channel")
    }
}

func TestHandleHelloRejectsInvalidTransmitFormat(t *testing.T) {
    sm := state.NewManager()
    userID := "test-hello-2"
    user := &types.User{
        ID: userID,
        Username: "User_old",
        Capabilities: types.DefaultWebClientCapabilities(),
    }
    ws := &types.WebSocketConnection{
        UserID: userID,
        Send:   make(chan []byte, 10),
    }
    sm.AddUser(user)
    sm.AddClient(userID, ws)

    cm := &ConnectionManager{
        wsConn:       ws,
        user:         user,
        stateManager: sm,
        userID:       userID,
    }

    // Provide an invalid transmit format (unsupported codec and too many channels)
    caps := map[string]interface{}{
        "client_type": "custom",
        "user_agent":  "bad-agent",
        "transmit_format": map[string]interface{}{
            "codec":       "unsupported-codec",
            "bitrate":     9999999,
            "sample_rate": 12345,
            "channels":    8,
        },
    }

    ev := &types.PTTEvent{
        Type:      "hello",
        UserID:    userID,
        Timestamp: time.Now(),
        Data: map[string]interface{}{
            "username":     "bob",
            "channel":      "room1",
            "clientType":   "custom",
            "capabilities": caps,
        },
    }

    cm.handleHello(ev)

    // verify welcome queued and rejected
    select {
    case msg := <-ws.Send:
        var we types.PTTEvent
        if err := json.Unmarshal(msg, &we); err != nil {
            t.Fatalf("failed to unmarshal welcome: %v", err)
        }
        if we.Type != "welcome" {
            t.Fatalf("expected welcome type, got %s", we.Type)
        }
        if accepted, ok := we.Data["accepted"].(bool); !ok || accepted {
            t.Fatalf("expected accepted=false in welcome, got %v", we.Data["accepted"])
        }
        if _, ok := we.Data["reason"].(string); !ok {
            t.Fatalf("expected reason string in rejected welcome")
        }
    default:
        t.Fatalf("no message sent to client Send channel")
    }
}
