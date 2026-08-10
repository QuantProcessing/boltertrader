package httpapi

import (
	"crypto/sha256"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	loginFailureLimit       = 5
	loginFailureWindow      = 15 * time.Minute
	loginLockDuration       = 5 * time.Minute
	loginThrottleMaxEntries = 4096
)

type loginThrottleEntry struct {
	failures     []time.Time
	blockedUntil time.Time
	updatedAt    time.Time
}

type loginThrottle struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]loginThrottleEntry
	now     func() time.Time
}

func newLoginThrottle(now func() time.Time) *loginThrottle {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &loginThrottle{
		entries: make(map[[sha256.Size]byte]loginThrottleEntry),
		now:     now,
	}
}

func loginThrottleKey(email, remoteAddress string) [sha256.Size]byte {
	source := strings.TrimSpace(remoteAddress)
	if host, _, err := net.SplitHostPort(source); err == nil {
		source = host
	}
	if ip := net.ParseIP(source); ip != nil {
		source = ip.String()
	}
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	return sha256.Sum256([]byte(normalizedEmail + "\x00" + source))
}

func (throttle *loginThrottle) RetryAfter(key [sha256.Size]byte) time.Duration {
	if throttle == nil {
		return 0
	}
	now := throttle.now().UTC()
	throttle.mu.Lock()
	defer throttle.mu.Unlock()

	entry, exists := throttle.entries[key]
	if !exists {
		return 0
	}
	if entry.blockedUntil.After(now) {
		return entry.blockedUntil.Sub(now)
	}
	entry.failures = recentLoginFailures(entry.failures, now)
	if len(entry.failures) == 0 {
		delete(throttle.entries, key)
		return 0
	}
	entry.blockedUntil = time.Time{}
	entry.updatedAt = now
	throttle.entries[key] = entry
	return 0
}

func (throttle *loginThrottle) RecordFailure(key [sha256.Size]byte) {
	if throttle == nil {
		return
	}
	now := throttle.now().UTC()
	throttle.mu.Lock()
	defer throttle.mu.Unlock()

	entry := throttle.entries[key]
	if entry.blockedUntil.After(now) {
		return
	}
	entry.failures = append(recentLoginFailures(entry.failures, now), now)
	entry.updatedAt = now
	if len(entry.failures) >= loginFailureLimit {
		entry.blockedUntil = now.Add(loginLockDuration)
		entry.failures = nil
	}
	if _, exists := throttle.entries[key]; !exists {
		throttle.ensureCapacity()
	}
	throttle.entries[key] = entry
}

func (throttle *loginThrottle) Reset(key [sha256.Size]byte) {
	if throttle == nil {
		return
	}
	throttle.mu.Lock()
	delete(throttle.entries, key)
	throttle.mu.Unlock()
}

func (throttle *loginThrottle) ensureCapacity() {
	if len(throttle.entries) < loginThrottleMaxEntries {
		return
	}
	var (
		oldestKey [sha256.Size]byte
		oldest    time.Time
	)
	for key, entry := range throttle.entries {
		if oldest.IsZero() || entry.updatedAt.Before(oldest) {
			oldestKey = key
			oldest = entry.updatedAt
		}
	}
	delete(throttle.entries, oldestKey)
}

func recentLoginFailures(failures []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-loginFailureWindow)
	first := 0
	for first < len(failures) && failures[first].Before(cutoff) {
		first++
	}
	if first == len(failures) {
		return nil
	}
	return failures[first:]
}
