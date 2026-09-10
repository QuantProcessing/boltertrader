package secretstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAccountCipherBindsMaterialToAccountAndVenue(t *testing.T) {
	cipher, err := NewCipher(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	material := []byte("offline-credential-canary")
	sealed, err := cipher.Seal("account-a", "binance", material)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, material) {
		t.Fatal("plaintext persisted")
	}
	restored, err := cipher.Open("account-a", "binance", sealed)
	if err != nil || !bytes.Equal(restored, material) {
		t.Fatal("round trip failed")
	}
	for _, scope := range [][2]string{{"account-b", "binance"}, {"account-a", "okx"}} {
		if _, err := cipher.Open(scope[0], scope[1], sealed); err == nil {
			t.Fatal("ciphertext accepted in another scope")
		}
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := cipher.Open("account-a", "binance", sealed); err == nil {
		t.Fatal("tampered credential accepted")
	}
	encoded, _ := json.Marshal(cipher)
	if strings.Contains(string(encoded), "offline") || !strings.Contains(fmt.Sprintf("%#v", cipher), "redacted") {
		t.Fatal("cipher formatting exposed material")
	}
}
