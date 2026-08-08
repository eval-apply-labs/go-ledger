package signing

// This file is the commented, non-default POST-QUANTUM scheme slot — the Go counterpart of the
// original DistributedLedger's algorithm switch, which sat commented out in application.properties:
//
//	#encryption.keyAlgo  = ML-DSA-87
//	#encryption.signAlgo = ML-DSA-87
//	encryption.keyAlgo   = RSA            <- the active default
//	encryption.signAlgo  = SHA256withRSA
//
// i.e. the post-quantum algorithm parked beside the active default, ready to switch on. Here it is a
// SLOT, not a flip-of-a-switch, and the difference is worth stating because it is the whole answer to
// "is it more complex?":
//
// In Java the algorithm is a STRING ("ML-DSA-87") resolved at runtime by a JCA provider, and a recent
// JDK ships ML-DSA in its standard provider — so uncommenting two lines WAS the change. Go has no such
// runtime provider registry, and — the load-bearing point — the Go standard library ships no ML-DSA
// signer. (crypto/mlkem, added in Go 1.24, is key ENCAPSULATION, not signatures; there is no
// crypto/mldsa to call, and this project targets Go 1.22.) The Scheme interface (scheme.go) already
// fits ML-DSA perfectly and NOTHING in the domain-separation / preimage layer would change — the only
// gap is the primitive. Filling it needs one of:
//
//  1. a standard-library crypto/mldsa, if/when it lands — then this file becomes a ~40-line Scheme
//     just like ed25519.go, and the ZERO-DEPENDENCY invariant (README) is preserved. Preferred.
//  2. an accepted third-party module (e.g. github.com/cloudflare/circl/sign/mldsa) — works TODAY, but
//     trades away that zero-dependency invariant, which is a core project value.
//
// That trade — a load-bearing project value against a capability with no value at village scale — is
// the real reason this stays commented rather than active, and why it is NOT a two-line flip like the
// Java version. The seam is done; the decision is deliberately deferred.
//
// SELECTION IS WHOLE-NETWORK, AT GENESIS — not per account, not per node, not hot-swappable. A
// signature is scheme-specific and every node must agree on the exact verifier (cf. "one Ed25519
// implementation is a consensus rule", docs/security.md), so the scheme is fixed at genesis. To switch
// a deployment: pass signing.MLDSA87{} instead of signing.Ed25519{} wherever the scheme is chosen —
// the node's Config.Scheme, the wallet, and cmd/keygen — and regenerate every key. ML-DSA keys
// (~2.6 KB) and signatures (~4.6 KB) are much larger than Ed25519's 32/64 bytes; nothing else in the
// framing changes (base64 key files and receipts just get bigger).
//
// "Fixed at genesis" does NOT mean an existing network is stuck on Ed25519 forever. The ledger is
// historyless and portable, so a live community can MIGRATE by snapshotting the agreed state and
// reseeding a fresh genesis under the new scheme (balances/supply carry over exactly — a currency
// continuation, not a reset). That is a planned ~couple-of-hours outage, not a hard fork, and it is a
// real governance decision the users own — so activating this slot the day Ed25519 looks at risk is a
// question of will, not of a wall. Procedure: docs/operating-a-network.md, "Migrating the ledger".
//
// Skeleton, ready to uncomment once a primitive from (1) or (2) is chosen (shown with circl; exact API
// names vary by version — treat this as the SHAPE, not gospel, and add a round-trip + cross-scheme
// rejection test mirroring signing_test.go):
//
//	import (
//		"crypto/rand"
//		mldsa "github.com/cloudflare/circl/sign/mldsa/mldsa87" // or the stdlib path when it exists
//	)
//
//	// MLDSA87 is ML-DSA-87 (FIPS 204, NIST security level 5) — a lattice signature that resists
//	// quantum attack. A drop-in Scheme; only the key/signature sizes differ from Ed25519.
//	type MLDSA87 struct{}
//
//	func (MLDSA87) Name() string { return "ml-dsa-87" }
//
//	func (MLDSA87) GenerateKey() (pub, priv []byte, err error) {
//		pk, sk, err := mldsa.GenerateKey(rand.Reader)
//		if err != nil {
//			return nil, nil, err
//		}
//		pb, err := pk.MarshalBinary()
//		if err != nil {
//			return nil, nil, err
//		}
//		sb, err := sk.MarshalBinary()
//		if err != nil {
//			return nil, nil, err
//		}
//		return pb, sb, nil
//	}
//
//	func (MLDSA87) Sign(priv, message []byte) ([]byte, error) {
//		sk := new(mldsa.PrivateKey)
//		if err := sk.UnmarshalBinary(priv); err != nil {
//			return nil, err
//		}
//		return mldsa.Scheme().Sign(sk, message, nil), nil
//	}
//
//	func (MLDSA87) Verify(pub, message, sig []byte) bool {
//		pk := new(mldsa.PublicKey)
//		if err := pk.UnmarshalBinary(pub); err != nil {
//			return false // malformed key: reject, never panic (Scheme contract)
//		}
//		return mldsa.Scheme().Verify(pk, message, sig, nil)
//	}
