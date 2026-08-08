package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"strconv"
)

// AssumedGossipHopMs is a conservative per-hop budget: how long one relay hop of gossip is assumed to
// take. It is used only to check that a governed step timeout is long enough for a proposal/vote to
// fan out across the dimension the topology implies — see LedgerParameters.timingConsistent.
const AssumedGossipHopMs = 250

// LedgerParameters are the network's operating parameters — part of COMMITTED STATE (so every node
// agrees on them and the digest covers them) and amendable by governance (a > ⅔ stake vote; see
// docs/governance.md). Beyond the transaction cost they carry the network TIMING PROFILE: the
// exchange-window cadence, the per-step consensus timeout, and the gossip fan-out. Those three are
// interconnected — the step timeout must be long enough for gossip to reach the nodes the fan-out
// implies within a window — so they are governed together as one unit, guarded by timingConsistent.
type LedgerParameters struct {
	TransactionCost *big.Rat
	BlockIntervalMs int64 // min wall time between blocks (the exchange-window cadence)
	StepTimeoutMs   int64 // base per-step consensus timeout (grows per round); bounds gossip propagation
	MaxPeers        int   // Hypernode gossip fan-out per node (0 = all-to-all); with participants sets the dimension
	// MaxMessageBytes caps the SERIALIZED size of a single payment or account update (0 = unbounded).
	// The transaction cost prices spam by count; this bounds it by size — without it, one fee buys a
	// permanently large ledger entry (a huge-denominator amount, a megabyte description) that every
	// validator stores and re-hashes into every subsequent state digest. Governed like the rest of
	// the profile; MinMessageBytes floors it so a vote cannot set it too small to fit any payment.
	MaxMessageBytes int
}

// MinMessageBytes is the smallest MaxMessageBytes governance may set: enough for a plain key-to-key
// payment (two keys, a signature, an amount and the JSON framing), so the bound can never be voted
// down to a value that bricks payments entirely.
const MinMessageBytes = 512

// fields is the canonical, length-prefixed field sequence used for both the digest and the vote
// preimage, so a change to any field moves the committed digest and is bound into every vote.
func (parameters LedgerParameters) fields() [][]byte {
	var transactionCost string
	if parameters.TransactionCost != nil {
		transactionCost = parameters.TransactionCost.RatString()
	}
	return [][]byte{
		[]byte(transactionCost),
		[]byte(strconv.FormatInt(parameters.BlockIntervalMs, 10)),
		[]byte(strconv.FormatInt(parameters.StepTimeoutMs, 10)),
		[]byte(strconv.Itoa(parameters.MaxPeers)),
		[]byte(strconv.Itoa(parameters.MaxMessageBytes)),
	}
}

// Digest is a deterministic hash of the parameters, so every node computes the same committed digest
// (and a governance vote can bind to the exact parameters it amends).
func (parameters LedgerParameters) Digest() [32]byte {
	hasher := sha256.New()
	for _, field := range parameters.fields() {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		hasher.Write(length[:])
		hasher.Write(field)
	}
	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))
	return digest
}

// TimingConsistent reports whether the step timeout is long enough for a proposal/vote to fan out to
// `participants` nodes at the configured gossip dimension — the load-bearing coupling between the
// exchange window and the number of nodes reachable within it. Moving the window changes how many
// nodes you can reach in it, and changing the fan-out changes how long a window must be; a governance
// change that would leave the timeout too short for the implied dimension (stranding votes and
// stalling consensus) is rejected. Also enforces basic bounds.
func (parameters LedgerParameters) TimingConsistent(participants int) bool {
	if parameters.StepTimeoutMs <= 0 || parameters.BlockIntervalMs < 0 || parameters.MaxPeers < 0 {
		return false
	}
	_, dimension := GroupParameters(participants, parameters.MaxPeers)
	if dimension < 1 {
		dimension = 1
	}
	return parameters.StepTimeoutMs >= int64(dimension)*AssumedGossipHopMs
}

// Acceptable reports whether the parameters are a well-formed operating profile for a network of
// `participants` nodes: a non-negative transaction cost and a timing-consistent window/fan-out.
func (parameters LedgerParameters) Acceptable(participants int) bool {
	if parameters.TransactionCost == nil || parameters.TransactionCost.Sign() < 0 {
		return false
	}
	if parameters.MaxMessageBytes != 0 && parameters.MaxMessageBytes < MinMessageBytes {
		return false // a bound too small to fit any payment would brick the network
	}
	return parameters.TimingConsistent(participants)
}

// LedgerState is the full committed state: the accounts, the operating parameters, and the governance
// sequence. Its Digest is the single value consensus commits (a block's StateDigest), so agreement on
// it is agreement on the balances/registry, the parameters in force, AND the governance sequence.
//
// GovernanceSequence is a monotonic counter incremented by each applied governance action (a parameter
// change or a balance reassignment). Every such certificate binds to the sequence it amends and is
// applied only while that sequence is current; applying it bumps the sequence, so the certificate is
// single-use and CANNOT be replayed after state cycles back (the value-bound guards alone could not
// guarantee that). Votes still accumulate freely because the sequence is stable between governance
// actions.
type LedgerState struct {
	Accounts           []Account
	Parameters         LedgerParameters
	GovernanceSequence int64
}

// Digest is the committed digest over accounts, parameters AND the governance sequence.
func (state LedgerState) Digest() [32]byte {
	accountsDigest := StateDigest(state.Accounts)
	parametersDigest := state.Parameters.Digest()
	hasher := sha256.New()
	hasher.Write(accountsDigest[:])
	hasher.Write(parametersDigest[:])
	var sequence [8]byte
	binary.BigEndian.PutUint64(sequence[:], uint64(state.GovernanceSequence))
	hasher.Write(sequence[:])
	var committedDigest [32]byte
	copy(committedDigest[:], hasher.Sum(nil))
	return committedDigest
}
