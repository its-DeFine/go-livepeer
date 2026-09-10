# Native external signing protocol (staging opt-in)

The native account manager uses an HTTP endpoint reached through an owner-controlled SSH loopback forward. It reads one bearer token from a local regular `0600` file; it never reads or creates a keystore. The endpoint receives `POST /v1/sign` with `Authorization: Bearer <token>` and JSON:

```json
{"account":"0x...","chainId":"42161","kind":"ticket|personal|transaction|typedData","payload":{}}
```

`account` and `chainId` are repeated on every request and are checked by the signing policy. The response for `ticket`, `personal`, and `typedData` is `{"signature":"0x<65-byte-signature>"}`. The response for `transaction` is `{"signedRawTx":"0x<rlp-or-typed-transaction>"}`.

A `ticket` payload is:

```json
{
  "messageHex":"0x<keccak-256-of-ticket-preimage>",
  "recipient":"0x...", "sender":"0x...",
  "faceValue":"<decimal>", "winProb":"<decimal>", "senderNonce":1,
  "recipientRandHash":"0x...", "creationRound":123,
  "creationRoundBlockHash":"0x..."
}
```

The preimage is the exact Solidity-packed sequence used by `pm.Ticket.Hash`: recipient address, sender address, 32-byte face value, 32-byte win probability, 32-byte sender nonce, recipient random hash, and (when present) 32-byte creation round followed by its 32-byte block hash. The native client recomputes the hash and verifies the recovered account. The policy must independently reconstruct the same hash and apply recipient, face value, expected value, round, and sender nonce limits before signing. A ticket is signed with the existing EIP-191 personal-message digest of `messageHex` bytes.

`personal` carries `{"messageHex":"0x<raw-message-bytes>"}` and is reserved by policy for the existing exact handshake message shapes. It must not be treated as permission to sign an arbitrary ticket hash. `typedData` carries the complete EIP-712 object (`types`, `primaryType`, `domain`, `message`) and is deny-by-default until explicitly allowlisted. `transaction` carries the canonical unsigned fields: numeric `type`, hex quantities `nonce`, `value`, `gas`, optional `gasPrice` or `maxFeePerGas`/`maxPriorityFeePerGas`, nullable `to`, hex `data`, and ordered `accessList` entries. The native client decodes the returned transaction, checks chain ID and recovered account, and compares every unsigned field before returning it to go-ethereum.
