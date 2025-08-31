package main

import (
    "net/http/httptest"
    "testing"

    "github.com/gin-gonic/gin"
    "github.com/segmentio/ksuid"
    cidpkg "whotalkie/internal/cid"
)

func TestCIDMiddlewareAddsHeader(t *testing.T) {
    router := gin.New()
    s := &Server{stateManager: nil}
    router.Use(s.cidMiddleware())
    router.GET("/ping", func(c *gin.Context) { c.String(200, "ok") })

    req := httptest.NewRequest("GET", "/ping", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    cid := w.Header().Get(cidpkg.HeaderName)
    if cid == "" {
        t.Fatalf("expected response to include header %s, but it was empty", cidpkg.HeaderName)
    }

    if _, err := ksuid.Parse(cid); err != nil {
        t.Fatalf("expected %s to be a valid ksuid, got parse error: %v", cid, err)
    }
}

func TestCIDMiddlewarePreservesExistingHeader(t *testing.T) {
    router := gin.New()
    s := &Server{stateManager: nil}
    router.Use(s.cidMiddleware())
    router.GET("/echo", func(c *gin.Context) { c.String(200, "ok") })

    // Provide a pre-existing CID header that should be preserved by middleware
    incoming := ksuid.New().String()
    req := httptest.NewRequest("GET", "/echo", nil)
    req.Header.Set(cidpkg.HeaderName, incoming)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    got := w.Header().Get(cidpkg.HeaderName)
    if got == "" {
        t.Fatalf("expected response to include header %s, but it was empty", cidpkg.HeaderName)
    }
    if got != incoming {
        t.Fatalf("expected middleware to preserve incoming CID %s, got %s", incoming, got)
    }
}
