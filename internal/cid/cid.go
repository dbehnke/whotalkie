package cid

import "context"

// ContextKey is the type used for storing CID in context to avoid collisions.
type ContextKey struct{}

// HeaderName is the HTTP header used to propagate the correlation id.
//
// Note: incoming requests that already include this header are respected by
// the server `cidMiddleware` and will be preserved (and propagated) rather
// than being replaced by a newly-generated KSUID. Use `AddHeaderFromContext`
// to add this header to outgoing requests when a CID exists on the context.
const HeaderName = "X-WT-CID"
// AttributeName is the span attribute key used to attach CID to spans.
const AttributeName = "wt.cid"

// WithCID returns a new context containing the provided correlation id.
func WithCID(ctx context.Context, cid string) context.Context {
	return context.WithValue(ctx, ContextKey{}, cid)
}

// CIDFromContext extracts the correlation id from context, if present.
func CIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ContextKey{}).(string); ok {
		return v
	}
	return ""
}

// AddHeaderFromContext adds the X-WT-CID header to the provided headers map if
// the context contains a CID. This centralizes header insertion logic.
func AddHeaderFromContext(headers map[string][]string, ctx context.Context) {
	if headers == nil {
		return
	}
	if cid := CIDFromContext(ctx); cid != "" {
		headers[HeaderName] = []string{cid}
	}
}
