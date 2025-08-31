package client

import (
	"context"
	"testing"

	cidpkg "whotalkie/internal/cid"
)

func TestBuildDialHeadersIncludesCID(t *testing.T) {
	ctx := cidpkg.WithCID(context.Background(), "unit-test-cid-42")
	h := buildDialHeaders(ctx, "test-agent/1.0")
	if got := h[cidpkg.HeaderName]; len(got) == 0 || got[0] != "unit-test-cid-42" {
		t.Fatalf("expected header %s=%s, got %v", cidpkg.HeaderName, "unit-test-cid-42", got)
	}
}
