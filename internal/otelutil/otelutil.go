package otelutil

import (
    "context"
    "fmt"
    "os"
    "strings"
    "time"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/propagation"
    stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
    otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    sdkresource "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

var tp *sdktrace.TracerProvider

// Init initializes a global tracer provider if WT_OTEL_STDOUT=1.
// It returns an error when no exporter is configured; callers may choose to ignore it.
func Init() error {
    ctx := context.Background()

    // Build common resource
    res, err := sdkresource.New(ctx, sdkresource.WithAttributes(
        semconv.ServiceNameKey.String("whotalkie"),
    ))
    if err != nil {
        return err
    }

    // Prefer OTLP/gRPC exporter when an endpoint is configured.
    endpoint := os.Getenv("WT_OTEL_OTLP_ENDPOINT")
    if endpoint == "" {
        endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
    }

    if endpoint != "" {
        return initWithOTLP(ctx, res, endpoint)
    }

    // Fallback: stdout exporter when requested
    if strings.ToLower(os.Getenv("WT_OTEL_STDOUT")) == "1" {
        return initWithStdout(ctx, res)
    }

    return fmt.Errorf("no OTEL exporter configured: set WT_OTEL_OTLP_ENDPOINT or WT_OTEL_STDOUT=1")
}

func initWithOTLP(ctx context.Context, res *sdkresource.Resource, endpoint string) error {
    opts := []otlptracegrpc.Option{
        otlptracegrpc.WithEndpoint(endpoint),
    }

    // allow insecure connections via env
    insecure := strings.ToLower(os.Getenv("WT_OTEL_OTLP_INSECURE")) == "1" || strings.ToLower(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")) == "1" || strings.ToLower(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")) == "true"
    if insecure {
        opts = append(opts, otlptracegrpc.WithInsecure())
    }

    // allow headers from OTEL_EXPORTER_OTLP_HEADERS (comma-separated key=val)
    if hdrs := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"); hdrs != "" {
        m := map[string]string{}
        for _, pair := range strings.Split(hdrs, ",") {
            kv := strings.SplitN(pair, "=", 2)
            if len(kv) == 2 {
                m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
            }
        }
        if len(m) > 0 {
            opts = append(opts, otlptracegrpc.WithHeaders(m))
        }
    }

    exporter, err := otlptracegrpc.New(ctx, opts...)
    if err != nil {
        return err
    }

    tp = sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(res),
    )

    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.TraceContext{})
    return nil
}

func initWithStdout(ctx context.Context, res *sdkresource.Resource) error {
    exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
    if err != nil {
        return err
    }

    tp = sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(res),
    )

    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.TraceContext{})
    return nil
}

// Flush gracefully shuts down the tracer provider, flushing any pending spans.
// It is safe to call multiple times.
func Flush() {
    if tp == nil {
        return
    }
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = tp.Shutdown(ctx)
}
