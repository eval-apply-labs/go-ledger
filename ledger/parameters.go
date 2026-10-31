package ledger

import (
	"math/big"
	"strconv"
)

// AssumedGossipHopMs is a conservative per-hop budget: how long one relay hop of gossip is assumed to
// take. It is used only to check that a governed step timeout is long enough for a proposal/vote to
// fan out across the dimension the topology implies — see LedgerParameters.TimingConsistent.
const AssumedGossipHopMs = 250

// MaxTimingMs caps BlockIntervalMs and StepTimeoutMs at one day, the longest window the documented
// pigeon mode uses (docs/managing-the-currency.md). A ratified profile — a typo included — can then
// hold back the next block, and the vote that would revert it, by about a day per step, not for good.
const MaxTimingMs = 24 * 60 * 60 * 1000

// LedgerParameters are the network's ten operating parameters — part of COMMITTED STATE (so every node
// agrees on them and the digest covers them) and amendable by governance (a > ⅔ stake vote; see
// docs/governance.md): the transaction cost; the TIMING PROFILE, which is the block interval, the
// per-step consensus timeout and the gossip fan-out; the message bound; the new-key fee; the change
// delay; the signature record's retention; and the largest payment and the largest balance. The step
// timeout must be long enough for gossip to reach the nodes the fan-out implies, and TimingConsistent
// checks the two together; the block interval is only bounded.
// Acceptable holds every parameter to the bounds in the comments, and ledger code relies on them: the
// genesis and every parameter change pass it, so no amount is nil and no delay is zero.
type LedgerParameters struct { // implements json.Marshaler and json.Unmarshaler, in amount.go
	TransactionCost *big.Rat // burned per payment; >= 0
	BlockIntervalMs int64    // min wall time between blocks (the exchange-window cadence); 0..MaxTimingMs
	StepTimeoutMs   int64    // base per-step consensus timeout (grows per round); 1..MaxTimingMs, and covers the gossip fan-out
	MaxPeers        int64    // Hypernode gossip fan-out per node (0 = all-to-all); 0..MaxPeersLimit
	// MaxMessageBytes caps the SERIALIZED size of a single payment or account update (0 = unbounded).
	// The transaction cost prices payments by count; this bounds both by size — without it, one free
	// account update writes a permanently large ledger entry (a megabyte description) that every
	// validator stores and re-hashes into every subsequent state digest. Governed like the rest of the
	// profile; MinMessageBytes floors it so a vote cannot set it too small to fit any payment. 0 or
	// MinMessageBytes..MaxMessageBytesLimit.
	MaxMessageBytes   int64
	NewKeyFee         *big.Rat // > 0; the fee of a registration, a key change or a bank change
	ChangeDelayBlocks int64    // 1..MaxChangeDelayBlocks; how many blocks a key or bank change waits
	RetainBlocks      int64    // MinRetainBlocks..MaxRetainBlocks; how long the signature record keeps a block
	MaxPaymentAmount  *big.Rat // > 0; the largest single payment
	MaxAccountBalance *big.Rat // > 0; the largest balance a payment or the ⅔ may raise an account to
}

// MinMessageBytes is the smallest MaxMessageBytes governance may set: enough for the largest payment
// whose comment needs no escaping (spec 4.1: a 280-byte comment, two 32-byte names, a pin, a
// 27-character amount and a 19-digit block id, with an ML-DSA-65 signature: 4961 bytes), so the bound
// can never be voted down to a value that bricks payments.
const MinMessageBytes = 5000

// The ceilings and floors Acceptable holds the parameters to (spec 3.4). MaxMessageBytesLimit is the
// 1 MiB every route but the proposal's decodes a body under (the node's maxBodyBytes is this constant).
// The ceilings on the delay and the retention keep a height plus either far inside int64, and stop one
// vote, or one typo, from filing changes that never fall due: 30 days and ten years at one block per
// second.
const (
	MaxPeersLimit        = 1000
	MaxMessageBytesLimit = 1 << 20
	MaxChangeDelayBlocks = 2_592_000
	MinRetainBlocks      = 1000
	MaxRetainBlocks      = 315_360_000
)

// DefaultParameters are the parameters a new network starts from (spec 2.4); the demo and the test
// fixtures start from them too. The default MaxMessageBytes, 6400, fits that largest payment even with
// every comment byte escaped, as JSON writes <, > and & in six bytes (6361 bytes).
func DefaultParameters() LedgerParameters {
	return LedgerParameters{
		TransactionCost:   big.NewRat(1, 10),
		BlockIntervalMs:   1000,
		StepTimeoutMs:     3000,
		MaxPeers:          0,
		MaxMessageBytes:   6400,
		NewKeyFee:         big.NewRat(1, 1),
		ChangeDelayBlocks: 86400, // about a day at one block per second
		RetainBlocks:      604800,
		MaxPaymentAmount:  big.NewRat(100_000, 1),
		MaxAccountBalance: big.NewRat(10_000_000, 1),
	}
}

// fields is the canonical, length-prefixed field sequence used for both the digest and the vote
// preimage, so a change to any field moves the committed digest and is bound into every vote.
func (ledgerParameters LedgerParameters) fields() [][]byte {
	return [][]byte{
		[]byte(FormatAmount(ledgerParameters.TransactionCost)),
		[]byte(strconv.FormatInt(ledgerParameters.BlockIntervalMs, 10)),
		[]byte(strconv.FormatInt(ledgerParameters.StepTimeoutMs, 10)),
		[]byte(strconv.FormatInt(ledgerParameters.MaxPeers, 10)),
		[]byte(strconv.FormatInt(ledgerParameters.MaxMessageBytes, 10)),
		[]byte(FormatAmount(ledgerParameters.NewKeyFee)),
		[]byte(strconv.FormatInt(ledgerParameters.ChangeDelayBlocks, 10)),
		[]byte(strconv.FormatInt(ledgerParameters.RetainBlocks, 10)),
		[]byte(FormatAmount(ledgerParameters.MaxPaymentAmount)),
		[]byte(FormatAmount(ledgerParameters.MaxAccountBalance)),
	}
}

// Digest is a deterministic hash of the parameters (spec 3.5: the tag "go-ledger parameters v2", then
// the ten fields), so every node computes the same committed digest (and a governance vote can bind to
// the exact parameters it amends).
func (ledgerParameters LedgerParameters) Digest() [32]byte {
	return lengthPrefixedDigest(append([][]byte{[]byte("go-ledger parameters v2")}, ledgerParameters.fields()...)...)
}

// TimingConsistent reports whether the step timeout is long enough for a proposal/vote to fan out to
// `participants` nodes at the configured gossip dimension — the load-bearing coupling between the
// step timeout and the number of nodes reachable within it. A shorter step timeout reaches fewer nodes,
// and changing the fan-out changes how long the step timeout must be; a governance change that would
// leave the timeout too short for the implied dimension (stranding votes and stalling consensus) is
// rejected. It also holds the step timeout, the block interval and the fan-out to their bounds,
// including the MaxTimingMs ceiling.
func (ledgerParameters LedgerParameters) TimingConsistent(participants int) bool {
	if ledgerParameters.StepTimeoutMs <= 0 || ledgerParameters.BlockIntervalMs < 0 || ledgerParameters.MaxPeers < 0 ||
		ledgerParameters.StepTimeoutMs > MaxTimingMs || ledgerParameters.BlockIntervalMs > MaxTimingMs ||
		ledgerParameters.MaxPeers > MaxPeersLimit {
		return false
	}
	_, dimension := GroupParameters(participants, int(ledgerParameters.MaxPeers))
	if dimension < 1 {
		dimension = 1
	}
	return ledgerParameters.StepTimeoutMs >= int64(dimension)*AssumedGossipHopMs
}

// Acceptable reports whether the parameters are a well-formed operating profile for a network of
// `participants` nodes: every parameter within the bounds in the LedgerParameters comments (a
// non-negative transaction cost, positive fee and caps, the message bound, delay and retention between
// their floors and ceilings), and a timing-consistent window/fan-out.
func (ledgerParameters LedgerParameters) Acceptable(participants int) bool {
	positive := func(amount *big.Rat) bool { return amount != nil && amount.Sign() > 0 }
	switch {
	case ledgerParameters.TransactionCost == nil || ledgerParameters.TransactionCost.Sign() < 0:
		return false
	case ledgerParameters.MaxMessageBytes != 0 && (ledgerParameters.MaxMessageBytes < MinMessageBytes || ledgerParameters.MaxMessageBytes > MaxMessageBytesLimit):
		return false // a bound too small to fit any payment would brick the network
	case !positive(ledgerParameters.NewKeyFee) || !positive(ledgerParameters.MaxPaymentAmount) || !positive(ledgerParameters.MaxAccountBalance):
		return false
	case ledgerParameters.ChangeDelayBlocks < 1 || ledgerParameters.ChangeDelayBlocks > MaxChangeDelayBlocks:
		return false
	case ledgerParameters.RetainBlocks < MinRetainBlocks || ledgerParameters.RetainBlocks > MaxRetainBlocks:
		return false
	}
	return ledgerParameters.TimingConsistent(participants)
}
