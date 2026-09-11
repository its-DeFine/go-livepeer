package eth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/livepeer/go-livepeer/pm"
)

// ExternalAccountManagerConfig configures the opt-in HTTP loopback signer.
// The bearer token is read once from TokenFile and is never included in errors.
type ExternalAccountManagerConfig struct {
	Endpoint   string
	Account    ethcommon.Address
	ChainID    *big.Int
	TokenFile  string
	HTTPClient *http.Client
}

type externalAccountManager struct {
	account  accounts.Account
	chainID  *big.Int
	endpoint *url.URL
	token    string
	http     *http.Client
	unlocked bool
}

type externalSignRequest struct {
	Account string      `json:"account"`
	ChainID string      `json:"chainId"`
	Kind    string      `json:"kind"`
	Payload interface{} `json:"payload"`
}

type externalSignResponse struct {
	Signature   string `json:"signature"`
	SignedRawTx string `json:"signedRawTx"`
}

type externalPersonalPayload struct {
	MessageHex  string `json:"messageHex"`
	PreimageHex string `json:"preimageHex,omitempty"`
}

type externalTicketPayload struct {
	MessageHex             string `json:"messageHex"`
	Recipient              string `json:"recipient"`
	Sender                 string `json:"sender"`
	FaceValue              string `json:"faceValue"`
	WinProb                string `json:"winProb"`
	SenderNonce            uint32 `json:"senderNonce"`
	RecipientRandHash      string `json:"recipientRandHash"`
	CreationRound          int64  `json:"creationRound"`
	CreationRoundBlockHash string `json:"creationRoundBlockHash"`
}

type externalAccessTuple struct {
	Address     string   `json:"address"`
	StorageKeys []string `json:"storageKeys"`
}

type externalTransactionPayload struct {
	Type                 uint8                 `json:"type"`
	Nonce                string                `json:"nonce"`
	To                   *string               `json:"to"`
	Value                string                `json:"value"`
	Gas                  string                `json:"gas"`
	GasPrice             string                `json:"gasPrice,omitempty"`
	MaxFeePerGas         string                `json:"maxFeePerGas,omitempty"`
	MaxPriorityFeePerGas string                `json:"maxPriorityFeePerGas,omitempty"`
	Data                 string                `json:"data"`
	AccessList           []externalAccessTuple `json:"accessList"`
}

var _ AccountManager = (*externalAccountManager)(nil)
var _ pm.TicketSigner = (*externalAccountManager)(nil)

// NewExternalAccountManager creates an account manager that never opens or
// creates a local keystore. The caller must explicitly Unlock it before use.
func NewExternalAccountManager(cfg ExternalAccountManagerConfig) (AccountManager, error) {
	if cfg.Account == (ethcommon.Address{}) {
		return nil, fmt.Errorf("external signer account is required")
	}
	if cfg.ChainID == nil || cfg.ChainID.Sign() <= 0 {
		return nil, fmt.Errorf("external signer chain ID is required")
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("invalid external signer endpoint")
	}
	if endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
		return nil, fmt.Errorf("external signer endpoint must not contain query, fragment, or userinfo")
	}
	host := strings.ToLower(endpoint.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("external signer endpoint must be loopback")
	}
	if cfg.TokenFile == "" {
		return nil, fmt.Errorf("external signer token file is required")
	}
	token, err := readExternalSignerToken(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clientCopy := *httpClient
		httpClient = &clientCopy
	}
	// The bearer token must never be sent to a different URL through a redirect.
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("external signer redirects are forbidden")
	}
	return &externalAccountManager{
		account:  accounts.Account{Address: cfg.Account},
		chainID:  new(big.Int).Set(cfg.ChainID),
		endpoint: endpoint,
		token:    token,
		http:     httpClient,
	}, nil
}

func readExternalSignerToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read external signer token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("external signer token file must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("external signer token file must not be group or world accessible")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read external signer token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.IndexFunc(token, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) >= 0 {
		return "", fmt.Errorf("external signer token file must contain one non-empty token")
	}
	return token, nil
}

func (am *externalAccountManager) Unlock(_ string) error {
	am.unlocked = true
	return nil
}

func (am *externalAccountManager) Lock() error {
	am.unlocked = false
	return nil
}

func (am *externalAccountManager) ready() error {
	if !am.unlocked {
		return ErrLocked
	}
	return nil
}

func (am *externalAccountManager) CreateTransactOpts(gasLimit uint64) (*bind.TransactOpts, error) {
	if err := am.ready(); err != nil {
		return nil, err
	}
	return &bind.TransactOpts{
		From:     am.account.Address,
		GasLimit: gasLimit,
		Signer: func(address ethcommon.Address, tx *types.Transaction) (*types.Transaction, error) {
			if address != am.account.Address {
				return nil, fmt.Errorf("external signer account mismatch")
			}
			return am.SignTx(tx)
		},
	}, nil
}

func (am *externalAccountManager) SignTx(tx *types.Transaction) (*types.Transaction, error) {
	if err := am.ready(); err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, fmt.Errorf("cannot sign nil transaction")
	}
	response, err := am.request("transaction", externalTransactionPayloadFor(tx))
	if err != nil {
		return nil, err
	}
	if response.SignedRawTx == "" {
		return nil, fmt.Errorf("external signer transaction response missing signedRawTx")
	}
	raw, err := hexutil.Decode(response.SignedRawTx)
	if err != nil {
		return nil, fmt.Errorf("decode external signed transaction: %w", err)
	}
	signed := new(types.Transaction)
	if err := signed.UnmarshalBinary(raw); err != nil {
		return nil, fmt.Errorf("decode external signed transaction: %w", err)
	}
	if signed.ChainId() == nil || signed.ChainId().Cmp(am.chainID) != 0 {
		return nil, fmt.Errorf("external signer returned transaction for wrong chain")
	}
	signer := types.LatestSignerForChainID(am.chainID)
	from, err := types.Sender(signer, signed)
	if err != nil {
		return nil, fmt.Errorf("recover external transaction signer: %w", err)
	}
	if from != am.account.Address {
		return nil, fmt.Errorf("external signer returned transaction for wrong account")
	}
	if err := equalUnsignedTransactions(tx, signed); err != nil {
		return nil, err
	}
	return signed, nil
}

func (am *externalAccountManager) Sign(msg []byte) ([]byte, error) {
	if err := am.ready(); err != nil {
		return nil, err
	}
	response, err := am.request("personal", externalPersonalPayload{MessageHex: hexutil.Encode(msg)})
	if err != nil {
		return nil, err
	}
	return verifyExternalSignature(accounts.TextHash(msg), response.Signature, am.account.Address)
}

// SignWithPreimage sends a personal-sign request with the exact preimage that
// produced msgHash, while preserving the existing personal-sign digest.
func (am *externalAccountManager) SignWithPreimage(msgHash, preimage []byte) ([]byte, error) {
	if err := am.ready(); err != nil {
		return nil, err
	}
	if !bytes.Equal(crypto.Keccak256(preimage), msgHash) {
		return nil, fmt.Errorf("external signer preimage does not match message hash")
	}
	response, err := am.request("personal", externalPersonalPayload{
		MessageHex:  hexutil.Encode(msgHash),
		PreimageHex: hexutil.Encode(preimage),
	})
	if err != nil {
		return nil, err
	}
	return verifyExternalSignature(accounts.TextHash(msgHash), response.Signature, am.account.Address)
}

// SignTicket is the optional full-preimage path used by pm.Sender. It keeps
// the legacy local signer path unchanged while preventing opaque ticket-hash
// approval when an external signer is configured.
func (am *externalAccountManager) SignTicket(ticket *pm.Ticket) ([]byte, error) {
	if err := am.ready(); err != nil {
		return nil, err
	}
	if ticket == nil {
		return nil, fmt.Errorf("cannot sign nil ticket")
	}
	if ticket.Sender != am.account.Address {
		return nil, fmt.Errorf("ticket sender does not match external signer account")
	}
	payload := externalTicketPayload{
		MessageHex:             ticket.Hash().Hex(),
		Recipient:              ticket.Recipient.Hex(),
		Sender:                 ticket.Sender.Hex(),
		FaceValue:              ticket.FaceValue.String(),
		WinProb:                ticket.WinProb.String(),
		SenderNonce:            ticket.SenderNonce,
		RecipientRandHash:      ticket.RecipientRandHash.Hex(),
		CreationRound:          ticket.CreationRound,
		CreationRoundBlockHash: ticket.CreationRoundBlockHash.Hex(),
	}
	response, err := am.request("ticket", payload)
	if err != nil {
		return nil, err
	}
	return verifyExternalSignature(accounts.TextHash(ticket.Hash().Bytes()), response.Signature, am.account.Address)
}

func (am *externalAccountManager) SignTypedData(typedData apitypes.TypedData) ([]byte, error) {
	if err := am.ready(); err != nil {
		return nil, err
	}
	digest, err := typedDataDigest(typedData)
	if err != nil {
		return nil, err
	}
	response, err := am.request("typedData", typedData)
	if err != nil {
		return nil, err
	}
	return verifyExternalSignature(digest, response.Signature, am.account.Address)
}

func (am *externalAccountManager) Account() accounts.Account {
	return am.account
}

func (am *externalAccountManager) request(kind string, payload interface{}) (*externalSignResponse, error) {
	requestBody, err := json.Marshal(externalSignRequest{
		Account: am.account.Address.Hex(),
		ChainID: am.chainID.String(),
		Kind:    kind,
		Payload: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("encode external signer request: %w", err)
	}
	endpoint := *am.endpoint
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/sign"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.String(), strings.NewReader(string(requestBody)))
	if err != nil {
		return nil, fmt.Errorf("create external signer request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+am.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := am.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("external signer request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("external signer rejected request with HTTP %d", resp.StatusCode)
	}
	var response externalSignResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode external signer response: %w", err)
	}
	return &response, nil
}

func verifyExternalSignature(hash []byte, encoded string, account ethcommon.Address) ([]byte, error) {
	signature, err := hexutil.Decode(encoded)
	if err != nil || len(signature) != 65 {
		return nil, fmt.Errorf("external signer returned invalid signature")
	}
	signature = append([]byte(nil), signature...)
	if signature[64] == 0 || signature[64] == 1 {
		signature[64] += 27
	}
	if signature[64] != 27 && signature[64] != 28 {
		return nil, fmt.Errorf("external signer returned invalid signature recovery byte")
	}
	canonical := append([]byte(nil), signature...)
	canonical[64] -= 27
	pub, err := crypto.SigToPub(hash, canonical)
	if err != nil || pub == nil || crypto.PubkeyToAddress(*pub) != account {
		return nil, fmt.Errorf("external signer returned signature for wrong account")
	}
	return signature, nil
}

func typedDataDigest(typedData apitypes.TypedData) ([]byte, error) {
	domainSeparator, err := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
	if err != nil {
		return nil, err
	}
	typedDataHash, err := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
	if err != nil {
		return nil, err
	}
	rawData := []byte(fmt.Sprintf("\x19\x01%s%s", string(domainSeparator), string(typedDataHash)))
	return crypto.Keccak256(rawData), nil
}

func externalTransactionPayloadFor(tx *types.Transaction) externalTransactionPayload {
	payload := externalTransactionPayload{
		Type:       tx.Type(),
		Nonce:      hexutil.EncodeUint64(tx.Nonce()),
		Value:      hexutil.EncodeBig(tx.Value()),
		Gas:        hexutil.EncodeUint64(tx.Gas()),
		Data:       hexutil.Encode(tx.Data()),
		AccessList: make([]externalAccessTuple, 0, len(tx.AccessList())),
	}
	if to := tx.To(); to != nil {
		value := to.Hex()
		payload.To = &value
	}
	if tx.Type() == types.LegacyTxType || tx.Type() == types.AccessListTxType {
		payload.GasPrice = hexutil.EncodeBig(tx.GasPrice())
	} else {
		payload.MaxFeePerGas = hexutil.EncodeBig(tx.GasFeeCap())
		payload.MaxPriorityFeePerGas = hexutil.EncodeBig(tx.GasTipCap())
	}
	for _, entry := range tx.AccessList() {
		item := externalAccessTuple{Address: entry.Address.Hex(), StorageKeys: make([]string, 0, len(entry.StorageKeys))}
		for _, key := range entry.StorageKeys {
			item.StorageKeys = append(item.StorageKeys, key.Hex())
		}
		payload.AccessList = append(payload.AccessList, item)
	}
	return payload
}

func equalUnsignedTransactions(unsigned, signed *types.Transaction) error {
	if unsigned.Type() != signed.Type() || unsigned.Nonce() != signed.Nonce() || unsigned.Gas() != signed.Gas() {
		return fmt.Errorf("external signer changed transaction fields")
	}
	if !equalAddressPtr(unsigned.To(), signed.To()) || !equalBigInt(unsigned.Value(), signed.Value()) || !equalBigInt(unsigned.GasPrice(), signed.GasPrice()) || !equalBigInt(unsigned.GasFeeCap(), signed.GasFeeCap()) || !equalBigInt(unsigned.GasTipCap(), signed.GasTipCap()) || !bytes.Equal(unsigned.Data(), signed.Data()) || !equalAccessLists(unsigned.AccessList(), signed.AccessList()) {
		return fmt.Errorf("external signer changed transaction fields")
	}
	return nil
}

func equalAccessLists(left, right types.AccessList) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Address != right[i].Address || len(left[i].StorageKeys) != len(right[i].StorageKeys) {
			return false
		}
		for j := range left[i].StorageKeys {
			if left[i].StorageKeys[j] != right[i].StorageKeys[j] {
				return false
			}
		}
	}
	return true
}

func equalAddressPtr(left, right *ethcommon.Address) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func equalBigInt(left, right *big.Int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Cmp(right) == 0
}
