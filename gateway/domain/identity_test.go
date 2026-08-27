package domain

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

func TestIdentityGeneratorCreatesValidUUIDv7IDs(t *testing.T) {
	generator := NewIdentityGenerator(IdentityGeneratorOptions{
		Now:    func() time.Time { return time.UnixMilli(1710000000123) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x11}, 1024)),
	})

	commandID, err := generator.CommandID()
	if err != nil {
		t.Fatalf("CommandID: %v", err)
	}
	orderID, err := generator.GatewayOrderID()
	if err != nil {
		t.Fatalf("GatewayOrderID: %v", err)
	}
	eventID, err := generator.EventID()
	if err != nil {
		t.Fatalf("EventID: %v", err)
	}
	for name, run := range map[string]func() error{
		"command":       commandID.Validate,
		"gateway_order": orderID.Validate,
		"event":         eventID.Validate,
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err != nil {
				t.Fatalf("generated %s id is invalid: %v", name, err)
			}
		})
	}
}

func TestIdentityGeneratorIsConcurrentSafe(t *testing.T) {
	generator := NewIdentityGenerator(IdentityGeneratorOptions{
		Now:    func() time.Time { return time.UnixMilli(1710000000456) },
		Random: &deterministicReader{},
	})

	const count = 256
	var wg sync.WaitGroup
	ids := make(chan CommandID, count)
	errs := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := generator.CommandID()
			if err != nil {
				errs <- err
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("CommandID: %v", err)
	}
	seen := map[CommandID]bool{}
	for id := range ids {
		if err := id.Validate(); err != nil {
			t.Fatalf("invalid concurrent id %q: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("duplicate concurrent id %q", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("generated %d IDs, want %d", len(seen), count)
	}
}

func TestIdentityGeneratorReturnsRandomFailure(t *testing.T) {
	generator := NewIdentityGenerator(IdentityGeneratorOptions{
		Now:    func() time.Time { return time.UnixMilli(1710000000123) },
		Random: errReader{},
	})
	if _, err := generator.CommandID(); err == nil {
		t.Fatal("CommandID succeeded after random failure")
	}
}

type deterministicReader struct {
	mu   sync.Mutex
	next byte
}

func (reader *deterministicReader) Read(p []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for i := range p {
		reader.next++
		p[i] = reader.next
	}
	return len(p), nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
