package nado

import (
	"errors"
	"fmt"
	"testing"
)

func TestClientSenderUsesConfiguredWalletAndSubaccount(t *testing.T) {
	client, err := newNadoTestnetClient(t).WithCredentials(fmt.Sprintf("%064x", 1), "arb")
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Sender()
	if err != nil {
		t.Fatal(err)
	}
	want := BuildSender(client.Signer.GetAddress(), "arb")
	if got != want {
		t.Fatalf("sender=%q, want configured wallet/subaccount sender %q", got, want)
	}

	withoutCredentials := newNadoTestnetClient(t)
	if _, err := withoutCredentials.Sender(); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("sender without credentials err=%v, want ErrCredentialsRequired", err)
	}
}
