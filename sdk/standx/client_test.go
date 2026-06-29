package standx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithCredentialsRejectsInvalidPrivateKey(t *testing.T) {
	client := NewClient()

	_, err := client.WithCredentials("invalid")
	require.Error(t, err)
	require.Nil(t, client.signer)
}
