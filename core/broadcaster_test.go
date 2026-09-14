package core_test

import (
	"bytes"
	"testing"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/livepeer/go-livepeer/core"
	"github.com/livepeer/go-livepeer/eth"
	"github.com/stretchr/testify/require"
)

type legacyBroadcasterEthClient struct {
	*eth.StubClient
	signInput []byte
}

func (c *legacyBroadcasterEthClient) Sign(msg []byte) ([]byte, error) {
	c.signInput = append([]byte(nil), msg...)
	return []byte("legacy"), nil
}

type preimageBroadcasterEthClient struct {
	*eth.StubClient
	legacyInput []byte
	hash        []byte
	preimage    []byte
}

func (c *preimageBroadcasterEthClient) Sign(msg []byte) ([]byte, error) {
	c.legacyInput = append([]byte(nil), msg...)
	return []byte("legacy"), nil
}

func (c *preimageBroadcasterEthClient) SignWithPreimage(hash, preimage []byte) ([]byte, error) {
	c.hash = append([]byte(nil), hash...)
	c.preimage = append([]byte(nil), preimage...)
	return []byte("preimage"), nil
}

func TestBroadcasterSignUsesOptionalPreimage(t *testing.T) {
	client := &preimageBroadcasterEthClient{
		StubClient: &eth.StubClient{TranscoderAddress: ethcommon.HexToAddress("0x1")},
	}
	node, err := core.NewLivepeerNode(client, "", nil)
	require.NoError(t, err)

	message := []byte("segment metadata")
	got, err := core.NewBroadcaster(node).Sign(message)
	require.NoError(t, err)
	require.Equal(t, []byte("preimage"), got)
	require.Equal(t, crypto.Keccak256(message), client.hash)
	require.Equal(t, message, client.preimage)
	require.Empty(t, client.legacyInput)
}

func TestBroadcasterSignPreservesLegacyFallback(t *testing.T) {
	client := &legacyBroadcasterEthClient{
		StubClient: &eth.StubClient{TranscoderAddress: ethcommon.HexToAddress("0x1")},
	}
	node, err := core.NewLivepeerNode(client, "", nil)
	require.NoError(t, err)

	message := []byte("legacy metadata")
	got, err := core.NewBroadcaster(node).Sign(message)
	require.NoError(t, err)
	require.Equal(t, []byte("legacy"), got)
	require.True(t, bytes.Equal(crypto.Keccak256(message), client.signInput))
}
