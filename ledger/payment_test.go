package ledger

import (
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func signedPayment(t *testing.T, signer *signing.Signer, to, amount string) Payment {
	t.Helper()
	p := Payment{
		PublicKeyFrom: signer.PublicKeyBase64(),
		PublicKeyTo:   to,
		Amount:        rat(amount),
		Comment:       "coffee",
		BlockID:       2,
	}
	p, err := SignPayment(signer, p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return p
}

func TestVerifyPaymentRoundTrip(t *testing.T) {
	signer, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	p := signedPayment(t, signer, "bob", "3")
	if !VerifyPayment(signing.Ed25519{}, "mainnet", p) {
		t.Fatal("a validly-signed payment did not verify")
	}
}

func TestVerifyPaymentTamperedAmountRejected(t *testing.T) {
	signer, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	p := signedPayment(t, signer, "bob", "3")
	p.Amount = rat("300") // tamper after signing
	if VerifyPayment(signing.Ed25519{}, "mainnet", p) {
		t.Fatal("a tampered amount verified")
	}
}

func TestVerifyPaymentWrongNetworkRejected(t *testing.T) {
	signer, _ := signing.Generate(signing.Ed25519{}, "testnet")
	p := signedPayment(t, signer, "bob", "3")
	if VerifyPayment(signing.Ed25519{}, "mainnet", p) {
		t.Fatal("a testnet payment verified on mainnet")
	}
}

// You cannot forge a payment "from" an account whose key you do not hold: the payer key is
// part of the signed message AND is the verification key.
func TestVerifyPaymentForgedPayerRejected(t *testing.T) {
	attacker, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	victim, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	p := Payment{
		PublicKeyFrom: victim.PublicKeyBase64(), // claims to be from the victim
		PublicKeyTo:   "attacker",
		Amount:        rat("100"),
		BlockID:       2,
	}
	p, _ = SignPayment(attacker, p) // but signed with the attacker's key
	if VerifyPayment(signing.Ed25519{}, "mainnet", p) {
		t.Fatal("a payment 'from' the victim but signed by the attacker verified")
	}
}

func TestValidPaymentsFilters(t *testing.T) {
	signer, _ := signing.Generate(signing.Ed25519{}, "mainnet")
	good := signedPayment(t, signer, "bob", "3")
	bad := good
	bad.Signature = "" // unsigned
	got := ValidPayments(signing.Ed25519{}, "mainnet", []Payment{good, bad})
	if len(got) != 1 {
		t.Fatalf("want 1 valid payment, got %d", len(got))
	}
}
