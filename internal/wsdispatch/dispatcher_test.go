package wsdispatch

import (
	"sync"
	"testing"
	"time"
)

func TestDispatcherBuffersWhilePaused(t *testing.T) {
	dispatcher := NewDispatcher()
	dispatcher.Pause()

	var got []string
	dispatcher.Dispatch(func() { got = append(got, "one") })
	dispatcher.Dispatch(func() { got = append(got, "two") })

	if len(got) != 0 {
		t.Fatalf("paused dispatcher delivered events: %v", got)
	}
}

func TestDispatcherResumeDrainsBufferInOrderAfterHook(t *testing.T) {
	dispatcher := NewDispatcher()
	dispatcher.Pause()

	var got []string
	dispatcher.Dispatch(func() { got = append(got, "one") })
	dispatcher.Dispatch(func() { got = append(got, "two") })
	dispatcher.Resume(func() { got = append(got, "hook") })

	want := []string{"hook", "one", "two"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDispatcherDeliversImmediatelyWhenRunning(t *testing.T) {
	dispatcher := NewDispatcher()

	var got []string
	dispatcher.Dispatch(func() { got = append(got, "one") })

	if len(got) != 1 || got[0] != "one" {
		t.Fatalf("got %v, want [one]", got)
	}
}

func TestDispatcherBuffersMessagesArrivingDuringResumeBeforeLiveDelivery(t *testing.T) {
	dispatcher := NewDispatcher()
	dispatcher.Pause()

	var got []string
	dispatcher.Dispatch(func() { got = append(got, "one") })
	dispatcher.Resume(func() {
		got = append(got, "hook")
		dispatcher.Dispatch(func() { got = append(got, "two") })
	})

	want := []string{"hook", "one", "two"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDispatcherSerializesConcurrentDispatch(t *testing.T) {
	dispatcher := NewDispatcher()

	var wg sync.WaitGroup
	var mu sync.Mutex
	active := 0
	maxActive := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dispatcher.Dispatch(func() {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				mu.Unlock()

				time.Sleep(time.Millisecond)

				mu.Lock()
				active--
				mu.Unlock()
			})
		}()
	}
	wg.Wait()

	if maxActive != 1 {
		t.Fatalf("max concurrent deliveries=%d, want 1", maxActive)
	}
}
