package ledger

// Verifier PoCs (round 2, group names-ban). Everything goes through ProposeBlock + ApplyBlock with
// real Ed25519 signatures — the exact functions node.startHeight / onProposal / commit run.

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func ownerOf(accounts []Account, name string) string { return nameRegistry(accounts)[name] }

func short(k string) string {
	if len(k) > 8 {
		return k[:8]
	}
	return k
}

// apply runs one block through the production pair and fails the test if the block does not apply.
func apply(t *testing.T, scheme signing.Scheme, network string, h int64, s LedgerState, c BlockContent) (LedgerState, Block) {
	t.Helper()
	blk := ProposeBlock(scheme, network, h, s, c)
	next, ok := ApplyBlock(scheme, network, s, blk)
	if !ok {
		t.Fatalf("block %d did not apply", h)
	}
	return next, blk
}

// H2, deterministic two-block form: a thief holding the VICTIM's key renames the victim's account
// (block 5), then claims the released name from the thief's own account (block 6). A pay-by-name
// payment the payer signed EARLIER for block 7 (normal wallet lookahead) credits the thief. The
// victim's balance never moves.
func TestVerifyH2RenameThenClaimTwoBlocks(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	victim, _ := signing.Generate(scheme, network)
	thief, _ := signing.Generate(scheme, network)
	payer, _ := signing.Generate(scheme, network)
	V, T, P := victim.PublicKeyBase64(), thief.PublicKeyBase64(), payer.PublicKeyBase64()
	cost := rat("0.1")
	s := st([]Account{
		{PublicKey: V, Name: "alice", Amount: rat("10"), ValidatorNode: V},
		{PublicKey: T, Amount: rat("1"), ValidatorNode: T},
		{PublicKey: P, Amount: rat("50"), ValidatorNode: P},
	}, cost)

	// In flight before the attack: the payer signs "pay alice 20" for block 7.
	inFlight, _ := SignPayment(payer, Payment{PublicKeyFrom: P, ToName: "alice", Amount: rat("20"), BlockID: 7})

	// Block 5: an update signed with the STOLEN victim key renames the victim's account.
	rename, _ := SignAccountUpdate(victim, AccountUpdate{From: V, Name: "alice-old", BlockID: 5})
	s5, b5 := apply(t, scheme, network, 5, s, BlockContent{Updates: []AccountUpdate{rename}})
	if len(b5.Updates) != 1 {
		t.Fatalf("rename not carried")
	}
	// Block 6: the thief's own (funded) account claims the now-free name. Fee-free.
	claim, _ := SignAccountUpdate(thief, AccountUpdate{From: T, Name: "alice", BlockID: 6})
	s6, _ := apply(t, scheme, network, 6, s5, BlockContent{Updates: []AccountUpdate{claim}})
	// Block 7: the in-flight payment resolves on-chain to the new holder.
	if !RecipientResolves(s6.Accounts, inFlight) {
		t.Fatal("payment to 'alice' should resolve (intake/ProposeBlock check)")
	}
	s7, b7 := apply(t, scheme, network, 7, s6, BlockContent{Payments: []Payment{inFlight}})

	t.Logf("owner('alice') before=%s after=%s (victim=%s thief=%s); payments carried in block 7=%d",
		short(ownerOf(s.Accounts, "alice")), short(ownerOf(s7.Accounts, "alice")), short(V), short(T), len(b7.Payments))
	t.Logf("victim balance %s -> %s; thief balance %s -> %s",
		balance(s.Accounts, V).FloatString(2), balance(s7.Accounts, V).FloatString(2),
		balance(s.Accounts, T).FloatString(2), balance(s7.Accounts, T).FloatString(2))
	if ownerOf(s7.Accounts, "alice") == T && balance(s7.Accounts, T).Cmp(rat("20.9")) == 0 && balance(s7.Accounts, V).Cmp(rat("10")) == 0 {
		t.Errorf("H2 CONFIRMED: name moved victim->thief without a payment or vote; pay-by-name credited the thief")
	}
}

// H2, single-block form: order is the block's content order (= the proposer's update-pool arrival
// order). rename-then-claim moves the name in ONE block; claim-then-rename leaves it free for the
// next block (anyone can take it).
func TestVerifyH2SameBlockOrdering(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	victim, _ := signing.Generate(scheme, network)
	thief, _ := signing.Generate(scheme, network)
	V, T := victim.PublicKeyBase64(), thief.PublicKeyBase64()
	s := st([]Account{
		{PublicKey: V, Name: "alice", Amount: rat("10"), ValidatorNode: V},
		{PublicKey: T, Amount: rat("1"), ValidatorNode: T},
	}, rat("0.1"))
	rename, _ := SignAccountUpdate(victim, AccountUpdate{From: V, Name: "zz", BlockID: 5})
	claim, _ := SignAccountUpdate(thief, AccountUpdate{From: T, Name: "alice", BlockID: 5})

	a, _ := apply(t, scheme, network, 5, s, BlockContent{Updates: []AccountUpdate{rename, claim}})
	b, _ := apply(t, scheme, network, 5, s, BlockContent{Updates: []AccountUpdate{claim, rename}})
	t.Logf("rename,claim -> owner(alice)=%q ; claim,rename -> owner(alice)=%q (thief=%s)",
		short(ownerOf(a.Accounts, "alice")), short(ownerOf(b.Accounts, "alice")), short(T))
	if ownerOf(a.Accounts, "alice") == T && ownerOf(b.Accounts, "alice") == "" {
		t.Errorf("H2 same-block: rename-then-claim hijacks in one block; reverse order frees the name for the next block")
	}
}

// Voluntary-rename variant (no stolen key): the legitimate owner renames; a watcher who saw the
// gossiped rename claims the old name in the same block; a payment signed earlier to the OLD name
// for a later block credits the watcher.
func TestVerifyH2VoluntaryRenameFrontRun(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	owner, _ := signing.Generate(scheme, network)
	watcher, _ := signing.Generate(scheme, network)
	payer, _ := signing.Generate(scheme, network)
	O, W, P := owner.PublicKeyBase64(), watcher.PublicKeyBase64(), payer.PublicKeyBase64()
	s := st([]Account{
		{PublicKey: O, Name: "bakery", Amount: rat("10"), ValidatorNode: O},
		{PublicKey: W, Amount: rat("1"), ValidatorNode: W},
		{PublicKey: P, Amount: rat("50"), ValidatorNode: P},
	}, rat("0.1"))
	inFlight, _ := SignPayment(payer, Payment{PublicKeyFrom: P, ToName: "bakery", Amount: rat("5"), BlockID: 6})
	rename, _ := SignAccountUpdate(owner, AccountUpdate{From: O, Name: "bakery-2", BlockID: 5})
	claim, _ := SignAccountUpdate(watcher, AccountUpdate{From: W, Name: "bakery", BlockID: 5})
	s5, _ := apply(t, scheme, network, 5, s, BlockContent{Updates: []AccountUpdate{rename, claim}})
	s6, _ := apply(t, scheme, network, 6, s5, BlockContent{Payments: []Payment{inFlight}})
	t.Logf("watcher %s -> %s; owner %s -> %s", balance(s.Accounts, W).FloatString(2), balance(s6.Accounts, W).FloatString(2),
		balance(s.Accounts, O).FloatString(2), balance(s6.Accounts, O).FloatString(2))
	if balance(s6.Accounts, W).Cmp(rat("5.9")) == 0 {
		t.Errorf("voluntary rename releases the name; in-flight payment to the old name credited the watcher")
	}
}

// C02 natural-spend variant through the real block path: payer signs pay-by-name for block 6; in
// block 5 alice sends her EXACT balance to her new wallet (intake admits promised == balance) and a
// watcher's claim lands after payments. Mirrors the round-1 PoC; kept here so the verifier copy is
// self-contained.
func TestVerifyC02SpendToZeroThenClaim(t *testing.T) {
	scheme, network := signing.Ed25519{}, "testnet"
	alice, _ := signing.Generate(scheme, network)
	fresh, _ := signing.Generate(scheme, network)
	watcher, _ := signing.Generate(scheme, network)
	payer, _ := signing.Generate(scheme, network)
	A, F, W, P := alice.PublicKeyBase64(), fresh.PublicKeyBase64(), watcher.PublicKeyBase64(), payer.PublicKeyBase64()
	s := st([]Account{
		{PublicKey: A, Name: "alice", Amount: rat("10"), ValidatorNode: A},
		{PublicKey: W, Amount: rat("1"), ValidatorNode: W},
		{PublicKey: P, Amount: rat("50"), ValidatorNode: P},
	}, rat("0.1"))
	inFlight, _ := SignPayment(payer, Payment{PublicKeyFrom: P, ToName: "alice", Amount: rat("20"), BlockID: 6})
	move, _ := SignPayment(alice, Payment{PublicKeyFrom: A, PublicKeyTo: F, Amount: rat("10"), BlockID: 5})
	// Same-block re-registration by alice's NEW wallet (created by the move in this block) would also
	// work if ordered first — this checks which claim wins when both are present.
	aliceReclaim, _ := SignAccountUpdate(fresh, AccountUpdate{From: F, Name: "alice", BlockID: 5})
	claim, _ := SignAccountUpdate(watcher, AccountUpdate{From: W, Name: "alice", BlockID: 5})

	s5, _ := apply(t, scheme, network, 5, s, BlockContent{Payments: []Payment{move}, Updates: []AccountUpdate{claim, aliceReclaim}})
	s6, _ := apply(t, scheme, network, 6, s5, BlockContent{Payments: []Payment{inFlight}})
	t.Logf("after block 5 owner(alice)=%s (alice=%s fresh=%s watcher=%s); alice account present=%v",
		short(ownerOf(s5.Accounts, "alice")), short(A), short(F), short(W), has(s5.Accounts, A))
	t.Logf("watcher %s -> %s", balance(s.Accounts, W).FloatString(2), balance(s6.Accounts, W).FloatString(2))
	if ownerOf(s6.Accounts, "alice") == W {
		t.Errorf("C02 CONFIRMED: spend-to-zero released 'alice'; the first claim in block order (the watcher) took it")
	}
}
