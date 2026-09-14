# Payments

## Probabilistic Micropayments

A broadcaster uses a probabilistic micropayment protocol to pay for transcoding work done by orchestrators.

The details of the protocol can be found in the [specification](https://github.com/livepeer/wiki/blob/master/spec/streamflow/pm.md).

The client implementation of the protocol can be found in the [pm](https://github.com/livepeer/go-livepeer/tree/master/pm) package.

## Fee Estimation

A broadcaster estimates the fee required for a segment in [estimateFee()](https://github.com/livepeer/go-livepeer/blob/731f6a5954e3ea190b9c5f0139491aa31e854a0a/server/segment_rpc.go#L692).

Note: [This line](https://github.com/livepeer/go-livepeer/blob/8bf42baf3b363a03ee432dd1e9676e552e59f12c/server/segment_rpc.go#L709) sets the FPS for the outputs to 120 if transcoding is using passthrough FPS. The historical reason for this was that B did not have access to the FPS of the source segment because it did not do any probing so we just conservatively set the output FPS to 120 - this results in overpaying most of the time if the source FPS < 120, but also provides a higher likelihood that the estimated fee is always >= the actual fee. TODO: Consider removing this 120 FPS placeholder to stop overestimating the fee by so much.

At the moment, an orchestrator is only required to charge for encoding (and not for decoding) so the fee for a segment only depends on the # of pixels encoded i.e. the # of pixels in the set of transcoded outputs. For example, if an orchestrator transcodes a segment to 720p/30fps and 360p/30fps outputs then the # of pixels encoded would be (1280 * 720 * 30) + (640 * 360 * 120). The # of pixels can then be multiplied by the price per pixel to determine the fee for the segment.

A broadcaster needs to estimate the fee required for a segment before sending the segment so it can make sure that its balance with the orchestrator for the current session is sufficient to pay for the segment. If the estimated fee cannot be covered by broadcaster’s current balance with the orchestrator for the session then broadcaster will send a payment to increase its balance in order to pay for the segment. A payment consists of one or many probabilistic micropayment tickets - the sum of the expected value of each ticket in the payment is the overall value of the payment. The broadcaster can control the maximum value it will send with a payment using the `-maxTicketEV` flag - a higher value allows larger payments to be sent with the tradeoff of increased value at risk if transcoding for a segment is not completed (since an orchestrator is paid *before* it returns results).

A broadcaster uses the estimated fee to determine the # of tickets to include in a payment i.e. the overall payment value in [newBalanceUpdate()](https://github.com/livepeer/go-livepeer/blob/731f6a5954e3ea190b9c5f0139491aa31e854a0a/server/segment_rpc.go#L730). Internally, [StageUpdate()](https://github.com/livepeer/go-livepeer/blob/731f6a5954e3ea190b9c5f0139491aa31e854a0a/core/accounting.go#L34) is called which will calculate the # of tickets required - the sum of the expected value of the tickets needs to be >= `max(estimatedFee, ticketEV(O))` (see [here](https://github.com/livepeer/go-livepeer/blob/731f6a5954e3ea190b9c5f0139491aa31e854a0a/server/segment_rpc.go#L750)) where `ticketEV(O)` is the required expected value of tickets required by the orchestrator.

The session balance system between a broadcaster and orchestrator (see [here](https://github.com/livepeer/go-livepeer/blob/731f6a5954e3ea190b9c5f0139491aa31e854a0a/server/segment_rpc.go#L222) and [here](https://github.com/livepeer/go-livepeer/blob/731f6a5954e3ea190b9c5f0139491aa31e854a0a/server/segment_rpc.go#L457)) is used to keep track of how much a broadcaster has paid during a session and how much is owed to the orchestrator based on work performed. The broadcaster credits its session balance with a payment - if it overpays then it adds extra credit to the session balance. Then, the orchestrator debits the session balance with the actual fee for a segment which is calculated based on the actual # of output pixels for the segment. Any remaining amount in the balance (i.e. from over-crediting) can be used to cover future segments for the session.

## Guaranteed winning tickets in the Punch fork

The sender compatibility change at `a14f52396afa6b37b884095f8c2b6d8f12ee4d3f`
allows ticket expected value to equal face value. Such a ticket must have
`winProb = 2^256 - 1`; it still passes the existing expiry, ticket EV, total EV,
maximum face value and deposit/reserve checks. Existing probabilistic ticket
behavior remains supported. This is a fork feature, not a claim about every
upstream deployment.

For Punch's accepted upfront flow, the provider advertises a fixed integer-wei
quote and matching ticket parameters; the buyer accepts that exact amount.
The ticket is signed and delivered before covered compute admission. Ticket
acceptance is sufficient for that admission; an on-chain redemption is a
separate operation and can still fail for funding, expiry or chain reasons.

Runner pricing and ticket economics are separate configuration. Native runners
support USD quotes through the USD/ETH feed and `wei`/`fixed` quotes. Punch's
guaranteed path requires a fixed-wei obligation quote matching the accepted
order value. The operator may change prices for new offers, but changing a
native registry price does not update an accepted Punch offer or its digest.
The node-level `-ticketEV` and `-maxFaceValue` controls can be set to the same
amount for a bounded setup; they do not automatically follow each capability's
price. Preserve existing sessions and accepted terms when changing shared
settings.

Funding the sender wallet is distinct from funding its TicketBroker deposit
and reserve. Funding also does not change external signer caps or expiry.
The deployed Punch acceptance used one `50000000000000`-wei ticket; the finite
authorization was consumed. It is not permission for unlimited purchases.
