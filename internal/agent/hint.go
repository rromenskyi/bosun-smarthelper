package agent

import "context"

type responseHintKey struct{}

// WithResponseHint attaches extra system-prompt guidance to one turn — e.g.
// voice devices ask for short spoken answers. Carried in the context so the
// Ask* signatures (and every existing caller) stay unchanged. Empty is a no-op.
func WithResponseHint(ctx context.Context, hint string) context.Context {
	if hint == "" {
		return ctx
	}
	return context.WithValue(ctx, responseHintKey{}, hint)
}

func responseHint(ctx context.Context) string {
	hint, _ := ctx.Value(responseHintKey{}).(string)
	return hint
}
