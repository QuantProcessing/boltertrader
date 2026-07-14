package perp

// Keep Adapter usable as a map key. Its private test seams must not silently
// make the exported value type non-comparable.
var _ = map[Adapter]struct{}{}
