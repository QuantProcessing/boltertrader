package sdk

// RequestOpts carries venue-native request options for low-level SDK methods.
// Adapters should not expose this type unless a stable cross-exchange
// abstraction has been designed for the specific option.
type RequestOpts struct {
	RecvWindowMillis int64
	ClientRequestID  string
}
