package wsdispatch

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDispatcherPausedBufferOverflowFailsClosed(t *testing.T) {
	dispatcher := NewBoundedDispatcher(2)
	dispatcher.Pause()

	var got []string
	if err := dispatcher.DispatchChecked(func() { got = append(got, "one") }); err != nil {
		t.Fatalf("dispatch one: %v", err)
	}
	if err := dispatcher.DispatchChecked(func() { got = append(got, "two") }); err != nil {
		t.Fatalf("dispatch two: %v", err)
	}
	if err := dispatcher.DispatchChecked(func() { got = append(got, "three") }); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("overflow error = %v, want %v", err, ErrBufferFull)
	}
	if buffered := len(dispatcher.buffer); buffered > 2 {
		t.Fatalf("buffered callbacks = %d, want at most 2", buffered)
	}

	dispatcher.Resume(nil)
	if len(got) != 0 {
		t.Fatalf("overflowed generation delivered partial callbacks: %v", got)
	}

	dispatcher.Reset()
	if err := dispatcher.DispatchChecked(func() { got = append(got, "after-reset") }); err != nil {
		t.Fatalf("dispatch after reset: %v", err)
	}
	if len(got) != 1 || got[0] != "after-reset" {
		t.Fatalf("callbacks after reset = %v, want [after-reset]", got)
	}
}

func TestDispatcherPausedBufferStaysBoundedWhileResumeHookBlocks(t *testing.T) {
	dispatcher := NewBoundedDispatcher(2)
	dispatcher.Pause()

	hookEntered := make(chan struct{})
	releaseHook := make(chan struct{})
	resumeDone := make(chan struct{})
	go func() {
		dispatcher.Resume(func() {
			close(hookEntered)
			<-releaseHook
		})
		close(resumeDone)
	}()
	select {
	case <-hookEntered:
	case <-time.After(time.Second):
		t.Fatal("resume hook did not start")
	}

	if err := dispatcher.DispatchChecked(func() {}); err != nil {
		t.Fatalf("dispatch one: %v", err)
	}
	if err := dispatcher.DispatchChecked(func() {}); err != nil {
		t.Fatalf("dispatch two: %v", err)
	}
	if err := dispatcher.DispatchChecked(func() {}); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("overflow error = %v, want %v", err, ErrBufferFull)
	}
	if buffered := len(dispatcher.buffer); buffered > 2 {
		t.Fatalf("buffered callbacks = %d, want at most 2", buffered)
	}

	close(releaseHook)
	select {
	case <-resumeDone:
	case <-time.After(time.Second):
		t.Fatal("overflowed resume did not stop after the hook returned")
	}
}

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

func TestDispatcherResetDropsPausedLifecycleBuffer(t *testing.T) {
	dispatcher := NewDispatcher()
	dispatcher.Pause()

	ran := false
	dispatcher.Dispatch(func() { ran = true })
	dispatcher.Reset()
	dispatcher.Resume(nil)
	if ran {
		t.Fatal("Reset delivered a callback buffered by the prior lifecycle")
	}
	dispatcher.Dispatch(func() { ran = true })
	if !ran {
		t.Fatal("Reset did not restore immediate delivery")
	}
}

func TestDispatcherResetIsReentrantFromResumeHook(t *testing.T) {
	dispatcher := NewDispatcher()
	dispatcher.Pause()

	ran := false
	dispatcher.Dispatch(func() { ran = true })
	done := make(chan struct{})
	go func() {
		dispatcher.Resume(func() { dispatcher.Reset() })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Reset deadlocked when called reentrantly from Resume hook")
	}
	if ran {
		t.Fatal("reentrant Reset delivered a callback buffered by the prior lifecycle")
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
