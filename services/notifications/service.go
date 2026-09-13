// Package notifications stores personal delivery channels and sends explicit
// notifications. Trading hosts it; it has no dependency on orders or venue SDKs.
package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalid     = errors.New("notification configuration is invalid")
	ErrNotFound    = errors.New("notification channel is not configured")
	ErrUnavailable = errors.New("notification settings are unavailable")
	ErrDelivery    = errors.New("notification provider rejected delivery")
)

type Provider string

const (
	Telegram Provider = "telegram"
	Bark     Provider = "bark"
)

func ValidProvider(p Provider) bool { return p == Telegram || p == Bark }

// Input is accepted only on writes. An omitted secret retains the saved one.
type Input struct {
	Secret    string `json:"secret"`
	Enabled   bool   `json:"enabled"`
	ChatID    string `json:"chatId,omitempty"`
	ServerURL string `json:"serverUrl,omitempty"`
	Critical  bool   `json:"critical"`
	Volume    int    `json:"volume"`
}

// Channel is the entire read surface. Credentials never leave this package.
type Channel struct {
	Provider  Provider  `json:"provider"`
	Enabled   bool      `json:"enabled"`
	ChatID    string    `json:"chatId,omitempty"`
	ServerURL string    `json:"serverUrl,omitempty"`
	Critical  bool      `json:"critical"`
	Volume    int       `json:"volume"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Record struct {
	Provider  Provider
	Sealed    []byte
	UpdatedAt time.Time
}

type Store interface {
	List(context.Context, string) ([]Record, error)
	Get(context.Context, string, Provider) (Record, error)
	Put(context.Context, string, Record) error
	Delete(context.Context, string, Provider) error
}

type Message struct{ Title, Body string }

type Service struct {
	store  Store
	box    cipher.AEAD
	sender *Sender
}

func New(store Store, key []byte, sender *Sender) (*Service, error) {
	if store == nil || len(key) != 32 {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	box, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	if sender == nil {
		sender = NewSender()
	}
	return &Service{store: store, box: box, sender: sender}, nil
}

func (s *Service) List(ctx context.Context, principal string) ([]Channel, error) {
	if s == nil || principal == "" {
		return nil, ErrUnavailable
	}
	records, err := s.store.List(ctx, principal)
	if err != nil {
		return nil, ErrUnavailable
	}
	result := make([]Channel, 0, len(records))
	for _, r := range records {
		input, err := s.open(principal, r)
		if err != nil {
			return nil, err
		}
		result = append(result, channel(r, input))
	}
	return result, nil
}

func (s *Service) Save(ctx context.Context, principal string, provider Provider, input Input) (Channel, error) {
	if s == nil || principal == "" {
		return Channel{}, ErrUnavailable
	}
	if !ValidProvider(provider) {
		return Channel{}, ErrInvalid
	}
	input.Secret = strings.TrimSpace(input.Secret)
	if input.Secret == "" {
		r, err := s.store.Get(ctx, principal, provider)
		if errors.Is(err, ErrNotFound) {
			return Channel{}, ErrInvalid
		}
		if err != nil {
			return Channel{}, ErrUnavailable
		}
		old, err := s.open(principal, r)
		if err != nil {
			return Channel{}, err
		}
		input.Secret = old.Secret
	}
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.ServerURL = strings.TrimRight(strings.TrimSpace(input.ServerURL), "/")
	if err := validate(provider, &input); err != nil {
		return Channel{}, err
	}
	plain, _ := json.Marshal(input)
	defer clear(plain)
	nonce := make([]byte, s.box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Channel{}, ErrUnavailable
	}
	r := Record{Provider: provider, UpdatedAt: time.Now().UTC(), Sealed: s.box.Seal(nonce, nonce, plain, associatedData(principal, provider))}
	if err := s.store.Put(ctx, principal, r); err != nil {
		return Channel{}, ErrUnavailable
	}
	return channel(r, input), nil
}

func (s *Service) Delete(ctx context.Context, principal string, provider Provider) error {
	if s == nil || principal == "" {
		return ErrUnavailable
	}
	if !ValidProvider(provider) {
		return ErrInvalid
	}
	if err := s.store.Delete(ctx, principal, provider); err != nil {
		return ErrUnavailable
	}
	return nil
}

// Test performs one delivery, including when automatic delivery is disabled.
// Saving does not contact a provider. A successful response means acceptance,
// not proof that the phone displayed or played the notification.
func (s *Service) Test(ctx context.Context, principal string, provider Provider) error {
	return s.deliver(ctx, principal, provider, Message{Title: "Bolter · 通知测试", Body: "通知通道已接通。这是一条手动发送的测试消息。"}, true)
}

// Send is the entry point for future monitoring rules; disabled channels do not send.
func (s *Service) Send(ctx context.Context, principal string, provider Provider, message Message) error {
	return s.deliver(ctx, principal, provider, message, false)
}

func (s *Service) deliver(ctx context.Context, principal string, provider Provider, message Message, test bool) error {
	if s == nil || principal == "" {
		return ErrUnavailable
	}
	if !ValidProvider(provider) {
		return ErrInvalid
	}
	r, err := s.store.Get(ctx, principal, provider)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	input, err := s.open(principal, r)
	if err != nil {
		return err
	}
	if !test && !input.Enabled {
		return nil
	}
	if len(message.Title)+len(message.Body) > 3500 || strings.TrimSpace(message.Body) == "" {
		return ErrInvalid
	}
	return s.sender.Send(ctx, provider, input, message)
}

func associatedData(principal string, provider Provider) []byte {
	return []byte("bolter-notification-v1\x00" + principal + "\x00" + string(provider))
}
func (s *Service) open(principal string, r Record) (Input, error) {
	n := s.box.NonceSize()
	if len(r.Sealed) < n+s.box.Overhead() {
		return Input{}, ErrUnavailable
	}
	plain, err := s.box.Open(nil, r.Sealed[:n], r.Sealed[n:], associatedData(principal, r.Provider))
	if err != nil {
		return Input{}, ErrUnavailable
	}
	defer clear(plain)
	var input Input
	if json.Unmarshal(plain, &input) != nil {
		return Input{}, ErrUnavailable
	}
	return input, nil
}
func channel(r Record, input Input) Channel {
	return Channel{Provider: r.Provider, Enabled: input.Enabled, ChatID: input.ChatID, ServerURL: input.ServerURL, Critical: input.Critical, Volume: input.Volume, UpdatedAt: r.UpdatedAt}
}

var botToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var chatID = regexp.MustCompile(`^(?:-?[0-9]+|@[A-Za-z0-9_]{5,})$`)
var deviceKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validate(provider Provider, input *Input) error {
	if len(input.Secret) > 512 {
		return ErrInvalid
	}
	switch provider {
	case Telegram:
		if !botToken.MatchString(input.Secret) || len(input.ChatID) > 128 || !chatID.MatchString(input.ChatID) {
			return ErrInvalid
		}
		input.ServerURL = ""
		input.Critical = false
		input.Volume = 0
	case Bark:
		if input.ServerURL == "" {
			input.ServerURL = "https://api.day.app"
		}
		if !deviceKey.MatchString(input.Secret) || input.Volume < 0 || input.Volume > 10 {
			return ErrInvalid
		}
		if err := validateServerURL(input.ServerURL); err != nil {
			return err
		}
		input.ChatID = ""
	default:
		return ErrInvalid
	}
	return nil
}

// MemoryStore is for uncredentialed tests; production uses encrypted SQL rows.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[string]Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{rows: make(map[string]Record)} }
func recordKey(principal string, provider Provider) string {
	return fmt.Sprintf("%s\x00%s", principal, provider)
}
func (s *MemoryStore) List(ctx context.Context, p string) ([]Record, error) {
	var rows []Record
	for _, provider := range []Provider{Telegram, Bark} {
		r, err := s.Get(ctx, p, provider)
		if err == nil {
			rows = append(rows, r)
		}
	}
	return rows, nil
}
func (s *MemoryStore) Get(_ context.Context, p string, v Provider) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[recordKey(p, v)]
	if !ok {
		return Record{}, ErrNotFound
	}
	r.Sealed = append([]byte(nil), r.Sealed...)
	return r, nil
}
func (s *MemoryStore) Put(_ context.Context, p string, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.Sealed = append([]byte(nil), r.Sealed...)
	s.rows[recordKey(p, r.Provider)] = r
	return nil
}
func (s *MemoryStore) Delete(_ context.Context, p string, v Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, recordKey(p, v))
	return nil
}
