package nado

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestEncodeSignedOrderFixture(t *testing.T) {
	signer, err := NewSigner(fmt.Sprintf("%064x", 1), 763373)
	if err != nil {
		t.Fatal(err)
	}
	order := TxOrder{
		Sender: BuildSender(signer.GetAddress(), "default"), ProductId: 2,
		PriceX18: "2500000000000000000000", Amount: "-100000000000000000",
		Expiration: "4000000000", Nonce: "1849300000000000000", Appendix: "1",
	}
	signature, _, err := signer.SignOrder(order, GenOrderVerifyingContract(2))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeSignedOrder(order, signature)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "0x") || len(encoded) < 500 {
		t.Fatalf("encoded order = %q", encoded)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(encoded))); got != "f5fadc0385d027195d6fdc4827391e575e048d8850d950b846e5b92f10d88d4b" {
		t.Fatalf("encoded order sha256 = %s", got)
	}
}

func TestEncodeSignedOrderRejectsMalformedInputs(t *testing.T) {
	order := TxOrder{
		Sender: "0x01", PriceX18: "1", Amount: "1", Expiration: "1", Nonce: "1", Appendix: "1",
	}
	if _, err := EncodeSignedOrder(order, "0x01"); err == nil {
		t.Fatal("malformed signed order unexpectedly encoded")
	}
}
