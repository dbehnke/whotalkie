package main

import (
    "context"
    "net/http/httptest"
    "testing"

    "github.com/gin-gonic/gin"
    cidpkg "whotalkie/internal/cid"
    "go.opentelemetry.io/otel"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOtelMiddlewareStartsSpan(t *testing.T) {
    // Setup in-memory exporter and tracer provider
    exp := tracetest.NewInMemoryExporter()
    tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
    otel.SetTracerProvider(tp)

    // Create server and register its otel middleware
    s := &Server{stateManager: nil}
    router := gin.New()
    router.Use(func(c *gin.Context) {
        // provide a base context
        c.Request = c.Request.WithContext(context.Background())
        c.Next()
    })
    router.Use(s.otelMiddleware())
    router.GET("/test", func(c *gin.Context) { c.String(200, "ok") })

    // Perform request
    req := httptest.NewRequest("GET", "/test", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    // Check that at least one span was recorded and contains HTTP attributes
    spans := exp.GetSpans()
    if len(spans) == 0 {
        t.Fatalf("expected spans to be recorded, got 0")
    }
    foundMethod := false
    foundTarget := false
    for _, s := range spans {
        for _, attr := range s.Attributes {
            if attr.Key == "http.method" && attr.Value.AsString() == "GET" {
                foundMethod = true
            }
            if attr.Key == "http.target" && attr.Value.AsString() == "/test" {
                foundTarget = true
            }
        }
    }
    if !foundMethod || !foundTarget {
        t.Fatalf("expected http.method and http.target attributes on spans; got method=%v target=%v", foundMethod, foundTarget)
    }
}

func TestOtelMiddlewareSetsCIDAttribute(t *testing.T) {
    // Setup in-memory exporter and tracer provider
    exp := tracetest.NewInMemoryExporter()
    tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
    otel.SetTracerProvider(tp)

    // Create server and register its otel middleware
    s := &Server{stateManager: nil}
    router := gin.New()
    router.Use(func(c *gin.Context) {
        // provide a base context with CID
        c.Request = c.Request.WithContext(cidpkg.WithCID(context.Background(), "test-cid-123"))
        c.Next()
    })
    router.Use(s.otelMiddleware())
    router.GET("/testcid", func(c *gin.Context) { c.String(200, "ok") })

    // Perform request
    req := httptest.NewRequest("GET", "/testcid", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

        // Check that at least one span was recorded and contains the attribute named by cidpkg.AttributeName
    spans := exp.GetSpans()
    if len(spans) == 0 {
        t.Fatalf("expected spans to be recorded, got 0")
    }
    foundCID := false
    for _, s := range spans {
        for _, attr := range s.Attributes {
            if attr.Key == cidpkg.AttributeName && attr.Value.AsString() == "test-cid-123" {
                foundCID = true
                break
            }
        }
        if foundCID {
            break
        }
    }
    if !foundCID {
        t.Fatalf("expected %s attribute on spans; not found", cidpkg.AttributeName)
    }
}
