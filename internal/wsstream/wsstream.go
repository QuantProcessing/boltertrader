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
	ch       chan T
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	closing  bool
	emitters sync.WaitGroup
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
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return false
	}
	s.emitters.Add(1)
	s.mu.Unlock()
	defer s.emitters.Done()

	select {
	case s.ch <- ev:
		return true
	case <-s.done:
		return false
	}
}

// Close stops the stream: blocked and future Emit calls unblock and return
// false. It waits for emitters that registered before shutdown, then closes the
// receive channel. Registration and shutdown are serialized so no sender can
// race with closing ch. Close is idempotent and concurrent-safe.
func (s *Stream[T]) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closing = true
		close(s.done)
		s.mu.Unlock()
		s.emitters.Wait()
		close(s.ch)
	})
}

// Done reports the channel that is closed when the stream closes, for consumers
// that want to stop reading.
func (s *Stream[T]) Done() <-chan struct{} { return s.done }
