package capability

import "context"

type metaKey struct{}

// Meta is the free key-value part of a request envelope (the "meta" field of
// the wire request). The platform carries it end to end and never interprets
// it; what the keys mean is agreed between whoever sets them and whoever reads
// them. Values are strings so every language can carry them unchanged.
type Meta map[string]string

// WithMeta returns a context that carries a copy of m. It replaces any meta
// already on ctx. An empty m returns ctx unchanged.
func WithMeta(ctx context.Context, m Meta) context.Context {
	if len(m) == 0 {
		return ctx
	}
	return context.WithValue(ctx, metaKey{}, copyMeta(m))
}

// MetaFrom returns a copy of the meta carried by ctx (nil if there is none).
func MetaFrom(ctx context.Context) Meta {
	m, _ := ctx.Value(metaKey{}).(Meta)
	if len(m) == 0 {
		return nil
	}
	return copyMeta(m)
}

func copyMeta(m Meta) Meta {
	cp := make(Meta, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// Reserved meta keys. The platform manages them automatically.
const (
	MetaTrace = "trace_id" // follows a call through every hop
	MetaDepth = "depth"    // how many platforms deep this call already is
)
