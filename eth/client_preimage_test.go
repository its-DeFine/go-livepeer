package eth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type signFallbackAccountManager struct {
	AccountManager
	input []byte
}

func (m *signFallbackAccountManager) Sign(msg []byte) ([]byte, error) {
	m.input = append([]byte(nil), msg...)
	return []byte("legacy"), nil
}

func TestClientSignWithPreimageFallsBackToSign(t *testing.T) {
	manager := &signFallbackAccountManager{}
	client := &client{accountManager: manager}
	msgHash := []byte("message hash")
	preimage := []byte("original message")

	got, err := client.SignWithPreimage(msgHash, preimage)
	require.NoError(t, err)
	require.Equal(t, []byte("legacy"), got)
	require.Equal(t, msgHash, manager.input)
}
