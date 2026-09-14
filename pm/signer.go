package pm

import "github.com/ethereum/go-ethereum/accounts"

// Signer supports identifying as an Ethereum account owner, by providing the
// Account and enabling message signing.
type Signer interface {
	Sign(msg []byte) ([]byte, error)
	Account() accounts.Account
}

// TicketSigner is an optional full-preimage signing path. Implementations
// receive the complete ticket so an external policy can reconstruct and
// authorize its hash before signing.
type TicketSigner interface {
	SignTicket(ticket *Ticket) ([]byte, error)
}
