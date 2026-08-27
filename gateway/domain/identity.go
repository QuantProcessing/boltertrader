package domain

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"
)

type IdentityGeneratorOptions struct {
	Now    func() time.Time
	Random io.Reader
}

type IdentityGenerator struct {
	now    func() time.Time
	random io.Reader
	mu     sync.Mutex
	seq    uint64
}

func NewIdentityGenerator(options IdentityGeneratorOptions) *IdentityGenerator {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	return &IdentityGenerator{now: now, random: random}
}

func (generator *IdentityGenerator) CommandID() (CommandID, error) {
	id, err := generator.uuidv7()
	return CommandID(id), err
}

func (generator *IdentityGenerator) GatewayOrderID() (GatewayOrderID, error) {
	id, err := generator.uuidv7()
	return GatewayOrderID(id), err
}

func (generator *IdentityGenerator) EventID() (EventID, error) {
	id, err := generator.uuidv7()
	return EventID(id), err
}

func (generator *IdentityGenerator) uuidv7() (string, error) {
	generator.mu.Lock()
	defer generator.mu.Unlock()

	var id [16]byte
	millis := uint64(generator.now().UnixMilli())
	id[0] = byte(millis >> 40)
	id[1] = byte(millis >> 32)
	id[2] = byte(millis >> 24)
	id[3] = byte(millis >> 16)
	id[4] = byte(millis >> 8)
	id[5] = byte(millis)
	if _, err := io.ReadFull(generator.random, id[6:]); err != nil {
		return "", fmt.Errorf("generate uuidv7 random bytes: %w", err)
	}
	generator.seq++
	seq := generator.seq
	for index := 0; index < 8; index++ {
		id[15-index] ^= byte(seq >> (index * 8))
	}
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(id[0:4]),
		binary.BigEndian.Uint16(id[4:6]),
		binary.BigEndian.Uint16(id[6:8]),
		binary.BigEndian.Uint16(id[8:10]),
		id[10:16],
	), nil
}
