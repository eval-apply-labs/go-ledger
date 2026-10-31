package node

// R3 verification (group R3-dos) for BE-NEW-2 / NEW-5, through the real HTTP intake of a 4-node network
// at the production default pool size (10000). Run: GOLEDGER_CHAOS_SLACK=4 go test -count=1 -run TestR3UpdPool -v ./node/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type r3http struct {
	nodes   []*Node
	servers []*httptest.Server
}

func startR3HTTP(t *testing.T, genesis []ledger.Account, pool int) *r3http {
	t.Helper()
	net := &r3http{}
	var signers []*signing.Signer
	vals := make([]consensus.Validator, 4)
	for i := range vals {
		s, _ := signing.Generate(signing.Ed25519{}, "testnet")
		signers = append(signers, s)
		vals[i] = consensus.Validator{PublicKey: s.PublicKeyBase64(), Power: 1}
	}
	vs := consensus.NewValidatorSet(vals)
	trs := make([]*HTTPTransport, 4)
	for i := range trs {
		trs[i] = NewHTTPTransport(nil)
		nd := New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i], Validators: vs,
			TransactionCost: rat("0.1"), Timeout: time.Second, MinBlockInterval: 200 * time.Millisecond, Genesis: genesis,
			MaxMempool: pool, MaxCommentLength: 280, MaxMessageBytes: 2000}, trs[i])
		net.nodes = append(net.nodes, nd)
		net.servers = append(net.servers, httptest.NewServer(NewServer(nd).Handler()))
	}
	for i := range trs {
		var peers []string
		for j := range net.servers {
			if j != i {
				peers = append(peers, net.servers[j].URL)
			}
		}
		trs[i].SetPeers(peers)
	}
	for _, nd := range net.nodes {
		nd.Start()
	}
	t.Cleanup(func() {
		for _, nd := range net.nodes {
			nd.Stop()
		}
		for _, s := range net.servers {
			s.Close()
		}
	})
	return net
}

func r3post(t *testing.T, client *http.Client, url string, v any) int {
	t.Helper()
	b, _ := json.Marshal(v)
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (net *r3http) heights() []int64 {
	var hs []int64
	for _, nd := range net.nodes {
		hs = append(hs, nd.Snapshot().Height)
	}
	return hs
}

func runR3UpdPool(t *testing.T, fill int) {
	const pool = 10000                                         // the production default (cmd/server/config.go)
	ghost, _ := signing.Generate(signing.Ed25519{}, "testnet") // holds no account anywhere
	owner, _ := signing.Generate(signing.Ed25519{}, "testnet")
	O := owner.PublicKeyBase64()
	net := startR3HTTP(t, []ledger.Account{{PublicKey: O, Amount: rat("100"), ValidatorNode: O}}, pool)
	waitFor(t, 10*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= 2 }, "live")
	client := &http.Client{Timeout: 10 * time.Second}

	start := time.Now()
	accepted := 0
	for i := 0; i < fill; i++ {
		u, _ := ledger.SignAccountUpdate(ghost, ledger.AccountUpdate{From: ghost.PublicKeyBase64(), Description: fmt.Sprintf("g%05d", i), BlockID: 1 << 40})
		if r3post(t, client, net.servers[0].URL+"/hdls/update", u) == http.StatusAccepted {
			accepted++
		}
	}
	if fill > 0 {
		t.Logf("ghost key: %d/%d updates got 202 at node0 in %v (heights %v)", accepted, fill, time.Since(start).Round(time.Millisecond), net.heights())
	}
	time.Sleep(3 * time.Second) // let relay settle and a few commits run dropBelow
	h0 := net.nodes[0].Snapshot().Height
	waitFor(t, 20*time.Second, func() bool { return net.nodes[0].Snapshot().Height >= h0+5 }, "commits continue")

	// The owner's registration is sent to ALL four nodes, so whichever proposes its block holds it.
	target := net.nodes[0].Snapshot().Height + 4
	reg, _ := ledger.SignAccountUpdate(owner, ledger.AccountUpdate{From: O, Name: "alice", BlockID: target})
	var codes []int
	for _, s := range net.servers {
		codes = append(codes, r3post(t, client, s.URL+"/hdls/update", reg))
	}
	waitFor(t, 30*time.Second, func() bool {
		for _, nd := range net.nodes {
			if nd.Snapshot().Height <= target+1 {
				return false
			}
		}
		return true
	}, "past the registration block")
	named := 0
	for _, nd := range net.nodes {
		for _, a := range nd.Snapshot().Accounts {
			if a.PublicKey == O && a.Name == "alice" {
				named++
			}
		}
	}
	t.Logf("fill=%d: owner's registration for block %d -> HTTP %v at the 4 nodes; registered on %d/4 nodes (heights %v)", fill, target, codes, named, net.heights())
}

func TestR3UpdPoolFillDefaultCap(t *testing.T) { runR3UpdPool(t, 10000) }
func TestR3UpdPoolControl(t *testing.T)        { runR3UpdPool(t, 0) }
