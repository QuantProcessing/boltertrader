package account

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/google/uuid"
)

var (
	ErrRegistrationNotFound = errors.New("account not found")
	ErrRegistrationConflict = errors.New("account request conflicts with an existing registration")
	ErrInvalidRegistration  = errors.New("invalid account registration")
)

// Registration is an immutable, validated exchange connection. The credential
// is stored atomically with this record, never in a second lifecycle journal.
type Registration struct {
	ID          domain.AccountID `json:"accountId"`
	Venue       string           `json:"venue"`
	DisplayName string           `json:"displayName"`
	CreatedAt   time.Time        `json:"createdAt"`
	ValidatedAt time.Time        `json:"validatedAt"`
}

type RegistrationRecord struct {
	Registration
	CredentialBundle []byte `json:"-"`
}

func (record RegistrationRecord) String() string {
	return "account.RegistrationRecord{credential:redacted}"
}
func (record RegistrationRecord) GoString() string { return record.String() }

type RegistrationStore interface {
	CreateRegistration(context.Context, RegistrationRecord) error
	GetRegistration(context.Context, domain.AccountID) (RegistrationRecord, error)
	ListRegistrations(context.Context) ([]Registration, error)
	DeleteRegistration(context.Context, domain.AccountID) error
}

// A prepared connection has passed the balance query but is not routable yet.
// Activation publishes it only after its account has been saved.
type RegistrationConnection interface {
	Activate() error
	Close() error
}

type RegistrationRuntime interface {
	PrepareRegistration(context.Context, RegistrationRecord) (RegistrationConnection, error)
	DisconnectRegistration(context.Context, domain.AccountID) error
}

type Registrations struct {
	store   RegistrationStore
	runtime RegistrationRuntime
	mu      sync.Mutex
}

func NewRegistrations(store RegistrationStore, runtime RegistrationRuntime) (*Registrations, error) {
	if store == nil || runtime == nil {
		return nil, fmt.Errorf("account registration dependencies are required")
	}
	return &Registrations{store: store, runtime: runtime}, nil
}

type AddRegistration struct {
	Venue            string `json:"venue"`
	DisplayName      string `json:"displayName"`
	CredentialBundle []byte `json:"-"`
}

func (service *Registrations) Add(ctx context.Context, request AddRegistration, requestID string) (Registration, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 256 || strings.ContainsAny(requestID, "\r\n\t ") ||
		strings.TrimSpace(request.DisplayName) == "" || len(request.DisplayName) > 128 || request.DisplayName != strings.TrimSpace(request.DisplayName) {
		return Registration{}, ErrInvalidRegistration
	}
	decoded, err := credential.DefaultRegistry().Decode(request.Venue, request.CredentialBundle)
	if err != nil {
		return Registration{}, ErrInvalidRegistration
	}
	decoded.Clear()
	// A replay uses the same account ID; it does not need an operation table.
	id := domain.AccountID("acct_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("bolter-account:"+requestID)).String())
	service.mu.Lock()
	defer service.mu.Unlock()
	previous, err := service.store.GetRegistration(ctx, id)
	if err == nil {
		defer clear(previous.CredentialBundle)
		if previous.Venue != request.Venue || previous.DisplayName != request.DisplayName || !bytes.Equal(previous.CredentialBundle, request.CredentialBundle) {
			return Registration{}, ErrRegistrationConflict
		}
		return previous.Registration, nil
	}
	if !errors.Is(err, ErrRegistrationNotFound) {
		return Registration{}, err
	}
	now := time.Now().UTC()
	record := RegistrationRecord{Registration: Registration{ID: id, Venue: request.Venue, DisplayName: request.DisplayName, CreatedAt: now}, CredentialBundle: append([]byte(nil), request.CredentialBundle...)}
	defer clear(record.CredentialBundle)
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	connection, err := service.runtime.PrepareRegistration(probeCtx, record)
	cancel()
	if err != nil {
		return Registration{}, err
	}
	if connection == nil {
		return Registration{}, errors.New("account connection unavailable")
	}
	activated := false
	defer func() {
		if !activated {
			_ = connection.Close()
		}
	}()
	record.ValidatedAt = time.Now().UTC()
	if err := service.store.CreateRegistration(ctx, record); err != nil {
		return Registration{}, err
	}
	if err := connection.Activate(); err != nil {
		// The record remains available for reconnect on the next startup. A
		// process shutdown must not erase a successfully stored credential.
		return record.Registration, err
	}
	activated = true
	return record.Registration, nil
}

func (service *Registrations) List(ctx context.Context) ([]Registration, error) {
	return service.store.ListRegistrations(ctx)
}

func (service *Registrations) Get(ctx context.Context, id domain.AccountID) (Registration, error) {
	record, err := service.store.GetRegistration(ctx, id)
	defer clear(record.CredentialBundle)
	return record.Registration, err
}

func (service *Registrations) Remove(ctx context.Context, id domain.AccountID) error {
	if id.Validate() != nil {
		return ErrInvalidRegistration
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.runtime.DisconnectRegistration(ctx, id); err != nil {
		return err
	}
	return service.store.DeleteRegistration(ctx, id)
}

// Restore reads the saved connections, not historical orders or local position
// accounting. One unavailable venue does not prevent healthy accounts starting.
func (service *Registrations) Restore(ctx context.Context) map[domain.AccountID]error {
	service.mu.Lock()
	defer service.mu.Unlock()
	failures := map[domain.AccountID]error{}
	items, err := service.store.ListRegistrations(ctx)
	if err != nil {
		failures[""] = err
		return failures
	}
	for _, item := range items {
		record, err := service.store.GetRegistration(ctx, item.ID)
		if err != nil {
			failures[item.ID] = err
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		connection, err := service.runtime.PrepareRegistration(probeCtx, record)
		cancel()
		clear(record.CredentialBundle)
		if err != nil {
			failures[item.ID] = err
			continue
		}
		if connection == nil {
			failures[item.ID] = errors.New("account connection unavailable")
			continue
		}
		if err := connection.Activate(); err != nil {
			_ = connection.Close()
			failures[item.ID] = err
		}
	}
	return failures
}
