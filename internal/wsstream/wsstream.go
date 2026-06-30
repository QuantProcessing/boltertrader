// Package wsstream provides a small, reusable event stream for adapter push
// channels. It guarantees that events are never silently dropped (sends block
// under backpressure, propagating it to the venue reader) while still being
// safely closable: after Close, sends unblock and become no-ops instead of
// panicking on a closed channel.
package wsstream

import "sync"

// Stream is a typed, closable event channel with backpressure. The zero value
// is not usable; call New.
type Stream[T any] struct {
	ch   chan T
	done chan struct{}
	once sync.Once
}

// New returns a Stream with the given buffer size.
func New[T any](buffer int) *Stream[T] {
	return &Stream[T]{
		ch:   make(chan T, buffer),
		done: make(chan struct{}),
	}
}

// C is the receive side handed to consumers.
func (s *Stream[T]) C() <-chan T { return s.ch }

// Emit delivers ev, blocking under backpressure until there is room OR the
// stream is closed. It NEVER drops ev silently and never panics after Close.
// Returns false if the stream was already closed (ev not delivered).
func (s *Stream[T]) Emit(ev T) bool {
	select {
	case s.ch <- ev:
		return true
	case <-s.done:
		return false
	}
}

// Close stops the stream: pending and future Emit calls unblock and return
// false. Idempotent. The underlying channel is intentionally not closed (so a
// concurrent Emit can never send on a closed channel); consumers should select
// on a context/done of their own, or stop reading after Close.
func (s *Stream[T]) Close() {
	s.once.Do(func() { close(s.done) })
}

// Done reports the channel that is closed when the stream closes, for consumers
// that want to stop reading.
func (s *Stream[T]) Done() <-chan struct{} { return s.done }
