package trader

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookie    = "bolter_session"
	sessionIdle      = 12 * time.Hour
	sessionAbsolute  = 7 * 24 * time.Hour
	argonTime        = 3
	argonMemoryKiB   = 64 * 1024
	argonThreads     = 2
	argonKeyLength   = 32
	maxLoginBackoff  = 15 * time.Minute
	loginFailureSpan = time.Hour
)

// PasswordHash is an encoded argon2id hash:
//
//	argon2id$v=19$m=65536,t=3,p=2$<salt b64>$<hash b64>
type PasswordHash string

func HashPassword(password string) (PasswordHash, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLength)
	return PasswordHash(fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))), nil
}

func (hash PasswordHash) Verify(password string) bool {
	parts := strings.Split(string(hash), "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// LoadOrCreatePassword reads the hash at path. When the file does not exist a
// random password is generated, its hash stored (0600), and the plaintext
// returned exactly once so the caller can print it.
func LoadOrCreatePassword(path string) (PasswordHash, string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr == nil && info.Mode().Perm()&0o077 != 0 {
			return "", "", fmt.Errorf("%s must not be readable by group or others (chmod 600)", path)
		}
		return PasswordHash(strings.TrimSpace(string(data))), "", nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	password := hex.EncodeToString(raw)
	hash, err := HashPassword(password)
	if err != nil {
		return "", "", err
	}
	if err := WritePassword(path, hash); err != nil {
		return "", "", err
	}
	return hash, password, nil
}

func WritePassword(path string, hash PasswordHash) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, []byte(string(hash)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

type session struct {
	created  time.Time
	lastSeen time.Time
}

// Sessions is an in-memory session table. A restart logs everyone out,
// which is acceptable for a single-operator tool and keeps no secret on disk.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]*session
	now      func() time.Time
}

func NewSessions() *Sessions {
	return &Sessions{sessions: map[string]*session{}, now: time.Now}
}

func (store *Sessions) Create() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := store.now()
	store.mu.Lock()
	store.sessions[token] = &session{created: now, lastSeen: now}
	store.mu.Unlock()
	return token, nil
}

// Valid reports whether token is a live session and refreshes its idle timer.
func (store *Sessions) Valid(token string) bool {
	if token == "" {
		return false
	}
	now := store.now()
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.sessions[token]
	if current == nil {
		return false
	}
	if now.Sub(current.lastSeen) > sessionIdle || now.Sub(current.created) > sessionAbsolute {
		delete(store.sessions, token)
		return false
	}
	current.lastSeen = now
	return true
}

func (store *Sessions) Delete(token string) {
	store.mu.Lock()
	delete(store.sessions, token)
	store.mu.Unlock()
}

// Throttle slows password guessing per client IP with exponential backoff.
type Throttle struct {
	mu       sync.Mutex
	failures map[string]*failure
	now      func() time.Time
}

type failure struct {
	count int
	last  time.Time
	until time.Time
}

func NewThrottle() *Throttle { return &Throttle{failures: map[string]*failure{}, now: time.Now} }

// Wait returns how long ip must wait before its next attempt.
func (throttle *Throttle) Wait(ip string) time.Duration {
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	entry := throttle.failures[ip]
	if entry == nil {
		return 0
	}
	if wait := entry.until.Sub(throttle.now()); wait > 0 {
		return wait
	}
	return 0
}

func (throttle *Throttle) Fail(ip string) {
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	now := throttle.now()
	entry := throttle.failures[ip]
	if entry == nil || now.Sub(entry.last) > loginFailureSpan {
		entry = &failure{}
		throttle.failures[ip] = entry
	}
	entry.count++
	entry.last = now
	delay := time.Second << min(entry.count-1, 20)
	if delay > maxLoginBackoff {
		delay = maxLoginBackoff
	}
	entry.until = now.Add(delay)
}

func (throttle *Throttle) Succeed(ip string) {
	throttle.mu.Lock()
	delete(throttle.failures, ip)
	throttle.mu.Unlock()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func retryAfterHeader(wait time.Duration) string {
	seconds := int(wait.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}
