package servicetoken

import "context"

func withIdentity(ctx context.Context, asserted identity) context.Context {
	return context.WithValue(ctx, identityKey{}, asserted)
}

func fromContext(ctx context.Context) (identity, bool) {
	if ctx == nil {
		return identity{}, false
	}
	asserted, ok := ctx.Value(identityKey{}).(identity)
	return asserted, ok && asserted.principal != ""
}
