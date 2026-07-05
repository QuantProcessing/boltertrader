package cloid

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

func ForClientID(clientID string) string {
	if isValidCloid(clientID) {
		return strings.ToLower(clientID)
	}
	sum := sha256.Sum256([]byte(clientID))
	return "0x" + hex.EncodeToString(sum[:16])
}

func isValidCloid(value string) bool {
	if len(value) != 34 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, r := range value[2:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

type Mapper struct {
	mu            sync.RWMutex
	clientByCloid map[string]string
	clientByOrder map[string]string
	cloidByClient map[string]string
	orderByClient map[string]string
}

func NewMapper() *Mapper {
	return &Mapper{
		clientByCloid: make(map[string]string),
		clientByOrder: make(map[string]string),
		cloidByClient: make(map[string]string),
		orderByClient: make(map[string]string),
	}
}

func (m *Mapper) VenueCloid(clientID string) string {
	venueCloid := ForClientID(clientID)
	m.Remember(clientID, venueCloid, "")
	return venueCloid
}

func (m *Mapper) Remember(clientID, venueCloid, venueOrderID string) {
	if m == nil || clientID == "" {
		return
	}
	venueCloid = normalizeCloid(venueCloid)
	m.mu.Lock()
	defer m.mu.Unlock()
	if venueCloid != "" {
		m.clientByCloid[venueCloid] = clientID
		m.cloidByClient[clientID] = venueCloid
	}
	if venueOrderID != "" {
		m.clientByOrder[venueOrderID] = clientID
		m.orderByClient[clientID] = venueOrderID
	}
}

func (m *Mapper) ClientID(venueCloid, venueOrderID string) string {
	if m == nil {
		if venueCloid != "" {
			return venueCloid
		}
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if venueOrderID != "" {
		if clientID, ok := m.clientByOrder[venueOrderID]; ok {
			return clientID
		}
	}
	if venueCloid != "" {
		if clientID, ok := m.clientByCloid[normalizeCloid(venueCloid)]; ok {
			return clientID
		}
		return venueCloid
	}
	return ""
}

func (m *Mapper) VenueCloidForClient(clientID string) string {
	if m == nil || clientID == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cloidByClient[clientID]
}

func normalizeCloid(value string) string {
	if isValidCloid(value) {
		return strings.ToLower(value)
	}
	return value
}
