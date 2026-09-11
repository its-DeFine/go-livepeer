package eth

import (
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/livepeer/go-livepeer/crypto"
	"github.com/livepeer/go-livepeer/pm"
	"github.com/stretchr/testify/require"
)

func TestExternalAccountManagerSignsTicketPersonalAndTransaction(t *testing.T) {
	key, err := ethcrypto.GenerateKey()
	require.NoError(t, err)
	account := ethcrypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(42161)
	tokenFile := filepath.Join(t.TempDir(), "signer.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("test-token\n"), 0600))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "/v1/sign", r.URL.Path)
		var request externalSignRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, account.Hex(), request.Account)
		require.Equal(t, chainID.String(), request.ChainID)

		switch request.Kind {
		case "personal":
			var payload externalPersonalPayload
			decodeExternalPayload(t, request.Payload, &payload)
			signExternalMessage(t, w, key, payload.MessageHex)
		case "ticket":
			var payload externalTicketPayload
			decodeExternalPayload(t, request.Payload, &payload)
			require.Equal(t, account.Hex(), payload.Sender)
			require.NotEmpty(t, payload.Recipient)
			require.Equal(t, "1000", payload.FaceValue)
			require.Equal(t, uint32(7), payload.SenderNonce)
			signExternalMessage(t, w, key, payload.MessageHex)
		case "transaction":
			var payload externalTransactionPayload
			decodeExternalPayload(t, request.Payload, &payload)
			require.Equal(t, uint8(types.LegacyTxType), payload.Type)
			require.Equal(t, "0x7", payload.Nonce)
			require.Equal(t, "0x2a", payload.Value)
			require.Equal(t, "0x5208", payload.Gas)
			require.Equal(t, "0x64", payload.GasPrice)
			value, err := hexutil.DecodeBig(payload.Value)
			require.NoError(t, err)
			gas, err := hexutil.DecodeUint64(payload.Gas)
			require.NoError(t, err)
			nonce, err := hexutil.DecodeUint64(payload.Nonce)
			require.NoError(t, err)
			gasPrice, err := hexutil.DecodeBig(payload.GasPrice)
			require.NoError(t, err)
			to := ethcommon.HexToAddress(*payload.To)
			tx := types.NewTransaction(nonce, to, value, gas, gasPrice, nil)
			signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(externalSignResponse{SignedRawTx: hexutil.Encode(mustMarshalTransaction(t, signed))}))
		default:
			http.Error(w, "unexpected kind", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	manager, err := NewExternalAccountManager(ExternalAccountManagerConfig{
		Endpoint:  server.URL,
		Account:   account,
		ChainID:   chainID,
		TokenFile: tokenFile,
	})
	require.NoError(t, err)
	_, err = manager.Sign([]byte("before unlock"))
	require.ErrorIs(t, err, ErrLocked)
	require.NoError(t, manager.Unlock(""))

	signature, err := manager.Sign([]byte("hello"))
	require.NoError(t, err)
	require.True(t, crypto.VerifySig(account, []byte("hello"), signature))

	ticket := &pm.Ticket{
		Recipient:              ethcommon.HexToAddress("0x1111111111111111111111111111111111111111"),
		Sender:                 account,
		FaceValue:              big.NewInt(1000),
		WinProb:                big.NewInt(500),
		SenderNonce:            7,
		RecipientRandHash:      ethcommon.HexToHash("0x2222"),
		CreationRound:          9,
		CreationRoundBlockHash: ethcommon.HexToHash("0x3333"),
	}
	ticketSigner := manager.(pm.TicketSigner)
	signature, err = ticketSigner.SignTicket(ticket)
	require.NoError(t, err)
	require.True(t, crypto.VerifySig(account, ticket.Hash().Bytes(), signature))

	to := ethcommon.HexToAddress("0x4444444444444444444444444444444444444444")
	unsigned := types.NewTransaction(7, to, big.NewInt(42), 21000, big.NewInt(100), nil)
	signed, err := manager.SignTx(unsigned)
	require.NoError(t, err)
	from, err := types.Sender(types.LatestSignerForChainID(chainID), signed)
	require.NoError(t, err)
	require.Equal(t, account, from)
}

func TestExternalAccountManagerSignWithPreimage(t *testing.T) {
	key, err := ethcrypto.GenerateKey()
	require.NoError(t, err)
	account := ethcrypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(42161)
	tokenFile := filepath.Join(t.TempDir(), "signer.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("test-token"), 0600))

	preimage := []byte("exact segment metadata")
	messageHash := ethcrypto.Keccak256(preimage)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request externalSignRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "personal", request.Kind)
		var payload externalPersonalPayload
		decodeExternalPayload(t, request.Payload, &payload)
		require.Equal(t, hexutil.Encode(messageHash), payload.MessageHex)
		require.Equal(t, hexutil.Encode(preimage), payload.PreimageHex)
		signExternalMessage(t, w, key, payload.MessageHex)
	}))
	defer server.Close()

	manager, err := NewExternalAccountManager(ExternalAccountManagerConfig{
		Endpoint:  server.URL,
		Account:   account,
		ChainID:   chainID,
		TokenFile: tokenFile,
	})
	require.NoError(t, err)
	require.NoError(t, manager.Unlock(""))
	signer, ok := manager.(interface {
		SignWithPreimage([]byte, []byte) ([]byte, error)
	})
	require.True(t, ok)

	_, err = signer.SignWithPreimage(ethcrypto.Keccak256([]byte("wrong")), preimage)
	require.ErrorContains(t, err, "preimage does not match message hash")
	require.Zero(t, requests)

	signature, err := signer.SignWithPreimage(messageHash, preimage)
	require.NoError(t, err)
	require.True(t, crypto.VerifySig(account, messageHash, signature))
	require.Equal(t, 1, requests)
}

func TestExternalAccountManagerRejectsRedirects(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "signer.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("token"), 0600))
	targetCalled := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalled <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/sign", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	manager, err := NewExternalAccountManager(ExternalAccountManagerConfig{
		Endpoint:   redirect.URL,
		Account:    ethcommon.HexToAddress("0x1111111111111111111111111111111111111111"),
		ChainID:    big.NewInt(42161),
		TokenFile:  tokenFile,
		HTTPClient: &http.Client{},
	})
	require.NoError(t, err)
	require.NoError(t, manager.Unlock(""))
	_, err = manager.Sign([]byte("redirect-check"))
	require.Error(t, err)
	select {
	case <-targetCalled:
		t.Fatal("redirect target received bearer request")
	default:
	}
}

func TestExternalAccountManagerRequiresLoopback(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "signer.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("token"), 0600))
	_, err := NewExternalAccountManager(ExternalAccountManagerConfig{
		Endpoint:  "https://signer.example.invalid",
		Account:   ethcommon.HexToAddress("0x1111111111111111111111111111111111111111"),
		ChainID:   big.NewInt(42161),
		TokenFile: tokenFile,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be loopback")
}

func TestExternalAccountManagerRejectsInsecureTokenFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "signer.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("token"), 0644))
	_, err := NewExternalAccountManager(ExternalAccountManagerConfig{
		Endpoint:  "http://127.0.0.1:1",
		Account:   ethcommon.HexToAddress("0x1111111111111111111111111111111111111111"),
		ChainID:   big.NewInt(42161),
		TokenFile: tokenFile,
	})
	require.Error(t, err)
}

func decodeExternalPayload(t *testing.T, raw interface{}, target interface{}) {
	t.Helper()
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, target))
}

func signExternalMessage(t *testing.T, w http.ResponseWriter, key *ecdsa.PrivateKey, messageHex string) {
	t.Helper()
	message, err := hexutil.Decode(messageHex)
	require.NoError(t, err)
	signature, err := ethcrypto.Sign(accounts.TextHash(message), key)
	require.NoError(t, err)
	signature[64] += 27
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(externalSignResponse{Signature: hexutil.Encode(signature)}))
}

func mustMarshalTransaction(t *testing.T, tx *types.Transaction) []byte {
	t.Helper()
	data, err := tx.MarshalBinary()
	require.NoError(t, err)
	return data
}
