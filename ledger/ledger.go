// Package ledger is the pure, side-effect-free core of the hypernode ledger: account state and the
// rules for applying a batch of payments, account updates and governance to move committed state from
// one to the next. It has no I/O and no networking (so it can be audited and unit-tested in complete
// isolation — the point of the Go rewrite), and it verifies signatures where the rules require it
// (block/payment/update/vote validity) via the `signing` package, but it holds no keys and does no
// key management of its own.
package ledger

import (
	"math/big"
	"sort"
)

// Account is one ledger account. Amount is an exact rational (arbitrary precision).
// No floating-point rounding in the money path.
type Account struct {
	PublicKey     string
	Name          string
	Amount        *big.Rat
	ValidatorNode string
	Description   string
	Address       string // a validator's gossip address (e.g. "http://10.0.0.2:8080") — the on-chain peer DNS
}

// Payment moves Amount from PublicKeyFrom to the recipient. An account's identity IS its public
// key, so PublicKeyFrom is both the payer and the key its Signature is verified against (see
// payment.go). Comment, BlockID and PrevStateDigest are all part of the signed fields.
//
// The recipient is EXACTLY ONE of PublicKeyTo (a raw key) or ToName (a registered on-chain name).
// A name is part of the signed content and is resolved against the committed registry by every node
// independently at apply time — the wallet's node never translates it, so a dishonest node cannot
// remap a name to a different key (the resolution the network agrees on is the one that pays).
type Payment struct {
	PublicKeyFrom string // payer's public key (base64); also the verification key
	PublicKeyTo   string // payee's public key (base64); empty when paying by name
	ToName        string // payee's registered name; resolved on-chain at apply time (empty when paying by key)
	Amount        *big.Rat
	Comment       string
	BlockID       int64
	// PrevStateDigest optionally pins the payment to a specific chain history: if non-empty, the
	// payment is included only in a block whose PrevStateDigest matches (hex). Empty = BlockID-only
	// (the default, and all an ordinary user needs). This is the fork-resistant tier: a payment
	// pinned to one history cannot be replayed on a fork whose block at the same height had a
	// different predecessor. It is meaningful only when the payer TRUSTS the node reporting the live
	// tip — in practice their OWN node: pinning to a tip an untrusted node told you buys nothing. So
	// this tier goes with self-hosting — run a node if you hold significant value; everyone else
	// relies on BlockID, which already prevents ordinary replay.
	PrevStateDigest string
	Signature       string // base64 payer signature over the canonical fields (domain "payment")
}

// ProcessPayments applies a batch of payments to a set of accounts and returns the new
// account set. It is a pure function of (accounts, payments, transactionCost); the input
// slices are not mutated. The result is sorted by public key for deterministic output.
//
// Integrity invariant (audit B1 / B2): a payment may move value only if its amount is at
// least the transaction cost (hence strictly positive) AND its payer is an existing account.
// Checking neither allows two ways to MINT supply:
//
//	B1  a negative amount is never overdraft-banned and the "credit - debit" step subtracts
//	    the negative, ballooning the payer's balance;
//	B2  a non-existent payer is synthesised at zero, driven negative and dropped, so the
//	    payee's credit is minted from nothing.
//
// This port enforces the guard up front so neither mint is representable.

// nameRegistry maps each registered name to its owning account key — the committed name registry
// (unique: applyAccountUpdates enforces first-come uniqueness).
func nameRegistry(accounts []Account) map[string]string {
	nameOwner := make(map[string]string)
	for _, a := range accounts {
		if a.Name != "" {
			nameOwner[a.Name] = a.PublicKey
		}
	}
	return nameOwner
}

// resolveRecipient returns the account key a payment credits and whether it names exactly one
// recipient that resolves against `nameOwner` (a pre-block registry). This is the SINGLE source of
// truth for recipient validity, shared by ProcessPayments (which credits the resolved key) and the
// block-layer filters (which refuse a payment that resolves to nobody, so it can never be carried
// into a committed block as a zero-fee no-op that still consumes block space and a per-node
// signature verification). A payment naming both a key and a name, or neither, is ambiguous → false.
func resolveRecipient(nameOwner map[string]string, p Payment) (to string, ok bool) {
	switch {
	case p.ToName != "" && p.PublicKeyTo != "":
		return "", false // ambiguous: both a key and a name
	case p.ToName != "":
		owner, registered := nameOwner[p.ToName]
		return owner, registered
	case p.PublicKeyTo != "":
		return p.PublicKeyTo, true // paying a raw key (the account may not exist yet — the credit creates it)
	default:
		return "", false // no recipient at all
	}
}

// RecipientResolves reports whether p names exactly one recipient resolvable against `accounts` (the
// pre-block state). ProposeBlock/ApplyBlock and node.admitPayment use it to keep an unresolvable
// payment out of a block entirely: the transaction fee is charged only to payments that actually
// move value, so a signature-valid payment to an unregistered name (or with an ambiguous/absent
// recipient) must not be able to occupy permanent block space for free.
func RecipientResolves(accounts []Account, p Payment) bool {
	_, ok := resolveRecipient(nameRegistry(accounts), p)
	return ok
}

func ProcessPayments(accounts []Account, payments []Payment, transactionCost *big.Rat) []Account {
	if transactionCost == nil || transactionCost.Sign() < 0 {
		// Treat an unset cost as zero rather than panicking on Cmp. A NEGATIVE cost is clamped for the
		// same conservation reason: it would credit each payee Amount-cost > Amount (minting |cost| per
		// payment) and let negative amounts through the below-cost filter. Config load and governance
		// (LedgerParameters.Acceptable) both refuse a negative cost; this keeps supply conserved even
		// for a caller that bypasses them.
		transactionCost = new(big.Rat)
	}
	byKey := make(map[string]Account, len(accounts))
	for _, a := range accounts {
		byKey[a.PublicKey] = a
	}
	nameOwner := nameRegistry(accounts)

	// B1/B2 guard: keep only strictly-valid payments; and collapse duplicate CONTENT so a malleated
	// signature (a second valid encoding of the same payment) can never debit the payer twice, even
	// in a hand-crafted block. Deterministic (first occurrence wins over the block's fixed order), so
	// every node computes the same result. Note: two payments the payer genuinely means as distinct
	// must differ in a field (e.g. the Comment) — identical content collapses to one.
	seen := make(map[string]bool)
	var valid []Payment
	var payee []string // resolved recipient key, parallel to valid
	for _, p := range payments {
		if p.Amount == nil || p.Amount.Cmp(transactionCost) < 0 {
			continue // nil, or below cost -- so zero and every negative are excluded
		}
		if _, ok := byKey[p.PublicKeyFrom]; !ok {
			continue // payer must exist
		}
		// Exactly one recipient form, resolved against the PRE-BLOCK registry (updates apply after
		// payments — see ApplyBlock), so every node picks the same owner even if the name moves in this
		// very block. An unresolvable recipient is dropped here — and, so it can never be a zero-fee
		// no-op occupying block space, also refused before the block by ProposeBlock/ApplyBlock (via
		// RecipientResolves) and at intake by node.admitPayment.
		to, ok := resolveRecipient(nameOwner, p)
		if !ok {
			continue
		}
		id := PaymentID(p)
		if seen[id] {
			continue // duplicate content (e.g. a malleated-signature copy) -- apply once
		}
		seen[id] = true
		valid = append(valid, p)
		payee = append(payee, to)
	}

	// Ban any payer whose total outgoing would overdraw their balance. Deliberate (see findings.md,
	// design decision): honest intake refuses such payments up front (node.admitPayment), so a
	// committed overdraw implies a misused/cloned key or a proposer that bypassed intake.
	outgoing := map[string]*big.Rat{}
	for _, p := range valid {
		addRat(outgoing, p.PublicKeyFrom, p.Amount)
	}
	banned := map[string]bool{}
	for key, out := range outgoing {
		if a, ok := byKey[key]; ok && a.Amount.Cmp(out) < 0 {
			banned[key] = true
		}
	}

	debit := map[string]*big.Rat{}  // payer -> total sent
	credit := map[string]*big.Rat{} // payee -> total received (net of the transaction cost)
	for i, p := range valid {
		if banned[p.PublicKeyFrom] {
			continue
		}
		addRat(debit, p.PublicKeyFrom, p.Amount)
		if p.Amount.Cmp(transactionCost) > 0 {
			addRat(credit, payee[i], new(big.Rat).Sub(p.Amount, transactionCost))
		}
	}

	affected := map[string]bool{}
	for k := range debit {
		affected[k] = true
	}
	for k := range credit {
		affected[k] = true
	}

	result := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		if !banned[a.PublicKey] && !affected[a.PublicKey] {
			result = append(result, a)
		}
	}
	for k := range affected {
		old, ok := byKey[k]
		if !ok {
			old = Account{PublicKey: k, Amount: new(big.Rat), ValidatorNode: k}
		}
		amt := new(big.Rat).Set(old.Amount)
		if c, ok := credit[k]; ok {
			amt.Add(amt, c)
		}
		if d, ok := debit[k]; ok {
			amt.Sub(amt, d)
		}
		if amt.Sign() > 0 { // non-positive balances are dropped
			na := old
			na.Amount = amt
			result = append(result, na)
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].PublicKey < result[j].PublicKey })
	return result
}

func addRat(m map[string]*big.Rat, key string, v *big.Rat) {
	if cur, ok := m[key]; ok {
		cur.Add(cur, v)
	} else {
		m[key] = new(big.Rat).Set(v)
	}
}
