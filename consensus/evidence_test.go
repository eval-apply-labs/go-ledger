package consensus

import "testing"

// The driver must surface a transferable, verifiable equivocation proof when a validator double-signs
// — which is what a node carries into a block to slash the offender.
func TestDriverSurfacesEquivocationEvidence(t *testing.T) {
	signers, vs := makeValidators(t, 4)
	me := signers[0]
	d := NewDriver(me, vs, scheme, network, 1, digest(0x01))
	d.Start()

	cheat := signers[1]
	d.ReceiveVote(mustVote(t, cheat, 1, 0, Prevote, digest(0x11)))
	d.ReceiveVote(mustVote(t, cheat, 1, 0, Prevote, digest(0x22))) // conflicting second vote

	ev := d.Evidence()
	found := false
	for _, e := range ev {
		if e.Validator == cheat.PublicKeyBase64() && e.Valid(scheme, network) {
			found = true
		}
	}
	if !found {
		t.Fatal("driver should surface valid equivocation evidence against the double-signer")
	}
}
