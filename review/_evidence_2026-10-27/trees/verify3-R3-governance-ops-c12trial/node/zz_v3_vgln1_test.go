package node

// Third-pass verifier PoC for VGL-N1, written from scratch. Everything goes through the real HTTP
// surface: httptest servers running Server.Handler(), HTTPTransport gossip between nodes, the wallet's
// exact single-vote POST shape for /hdls/reassignment-vote, the public GET /hdls/governance and
// GET /hdls/status, and the client POST /hdls/pay (with its overdraw guard). No internals are called.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/signing"
)

type v3Cluster struct {
	nodes   []*Node
	servers []*httptest.Server
	urls    []string
}

func (c *v3Cluster) stop() {
	for _, nd := range c.nodes {
		nd.Stop()
	}
	for _, s := range c.servers {
		s.Close()
	}
}

func v3StartHTTPCluster(t *testing.T, signers []*signing.Signer, set *consensus.ValidatorSet, genesis []ledger.Account, interval time.Duration) *v3Cluster {
	t.Helper()
	n := len(signers)
	c := &v3Cluster{nodes: make([]*Node, n), servers: make([]*httptest.Server, n), urls: make([]string, n)}
	transports := make([]*HTTPTransport, n)
	for i := range signers {
		transports[i] = NewHTTPTransport(nil)
		c.nodes[i] = New(Config{NetworkID: "testnet", Scheme: signing.Ed25519{}, Signer: signers[i], Validators: set,
			TransactionCost: rat("0.1"), Timeout: 2 * time.Second, MinBlockInterval: interval, Genesis: genesis}, transports[i])
		c.servers[i] = httptest.NewServer(NewServer(c.nodes[i]).Handler())
		c.urls[i] = c.servers[i].URL
	}
	for i := range signers {
		var peers []string
		for j := range signers {
			if j != i {
				peers = append(peers, c.urls[j])
			}
		}
		transports[i].SetPeers(peers)
	}
	for _, nd := range c.nodes {
		nd.Start()
	}
	return c
}

func v3Get(url string, v any) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func v3Post(url string, v any) (int, string) {
	b, _ := json.Marshal(v)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func v3Height(url string) int64 {
	var st struct {
		Height int64 `json:"height"`
	}
	_ = v3Get(url+"/hdls/status", &st)
	return st.Height
}

func v3Accounts(url string) []ledger.Account {
	var accts []ledger.Account
	_ = v3Get(url+"/hdls/accounts", &accts)
	return accts
}

func v3Gov(url string) GovernanceView {
	var view GovernanceView
	_ = v3Get(url+"/hdls/governance", &view)
	return view
}

type v3Setup struct {
	banks   []*signing.Signer
	set     *consensus.ValidatorSet
	thief   *signing.Signer
	stash   *signing.Signer
	T, X    string
	genesis []ledger.Account
}

func v3NewSetup() v3Setup {
	var s v3Setup
	vals := make([]consensus.Validator, 4)
	s.banks = make([]*signing.Signer, 4)
	for i := range s.banks {
		s.banks[i], _ = signing.Generate(signing.Ed25519{}, "testnet")
		vals[i] = consensus.Validator{PublicKey: s.banks[i].PublicKeyBase64(), Power: 1}
	}
	s.set = consensus.NewValidatorSet(vals) // quorum 3 of 4
	s.thief, _ = signing.Generate(signing.Ed25519{}, "testnet")
	s.stash, _ = signing.Generate(signing.Ed25519{}, "testnet")
	s.T, s.X = s.thief.PublicKeyBase64(), s.stash.PublicKeyBase64()
	s.genesis = []ledger.Account{{PublicKey: s.T, Name: "thief", Amount: rat("1000"), ValidatorNode: s.T}}
	for _, v := range vals {
		s.genesis = append(s.genesis, ledger.Account{PublicKey: v.PublicKey, Amount: rat("100"), ValidatorNode: v.PublicKey})
	}
	return s
}

// The wallet's VoteReassignment shape: one vote per POST (ui/admin.go:172-200).
func v3SingleVote(bank *signing.Signer, account string, prev, next *big.Rat, seq int64) ledger.Reassignment {
	vote, _ := ledger.SignReassignmentVote(bank, account, prev, next, seq)
	return ledger.Reassignment{Account: account, PreviousAmount: prev, NewAmount: next, Sequence: seq, Votes: []ledger.ReassignmentVote{vote}}
}

// thiefMovesEverything pays the thief's whole balance to the stash, through POST /hdls/pay on `url`,
// resubmitting for the current height until the funds have actually moved on the node's committed state.
func v3ThiefMovesEverything(t *testing.T, s v3Setup, url string, amount string, deadline time.Duration) (int64, bool) {
	t.Helper()
	end := time.Now().Add(deadline * testTimeSlack)
	var lastBlockID int64 = -1
	for time.Now().Before(end) {
		if ledger.AccountAmount(v3Accounts(url), s.X).Sign() > 0 {
			return lastBlockID, true
		}
		h := v3Height(url)
		if h != lastBlockID {
			pay, _ := ledger.SignPayment(s.thief, ledger.Payment{PublicKeyFrom: s.T, PublicKeyTo: s.X, Amount: rat(amount), BlockID: h})
			code, body := v3Post(url+"/hdls/pay", pay)
			if code == http.StatusOK {
				lastBlockID = h
			} else {
				t.Logf("pay for block %d refused: %d %s", h, code, body)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return lastBlockID, ledger.AccountAmount(v3Accounts(url), s.X).Sign() > 0
}

// VGL-N1 as the shipped wallet runs it: banks vote ONE AT A TIME (one vote per POST). The thief watches
// the public governance view on a DIFFERENT node (so gossip is exercised), and moves its funds as soon
// as the first vote appears — long before quorum.
func TestV3VGLN1OneAtATimeVotingTelegraphsSeizure(t *testing.T) {
	s := v3NewSetup()
	c := v3StartHTTPCluster(t, s.banks, s.set, s.genesis, 400*time.Millisecond)
	defer c.stop()
	waitFor(t, 10*time.Second, func() bool { return v3Height(c.urls[0]) >= 3 }, "network did not start")

	seq := v3Gov(c.urls[0]).GovernanceSequence
	prev, next := rat("1000"), rat("0")

	// Bank 0 casts the first vote (the afternoon begins).
	if code, body := v3Post(c.urls[0]+"/hdls/reassignment-vote", v3SingleVote(s.banks[0], s.T, prev, next, seq)); code != http.StatusAccepted {
		t.Fatalf("vote POST: %d %s", code, body)
	}
	// The thief, an ordinary key holder, polls GET /hdls/governance on node 3 (no credentials).
	var seen ReassignmentView
	waitFor(t, 5*time.Second, func() bool {
		for _, r := range v3Gov(c.urls[3]).Reassignments {
			if r.Account == s.T {
				seen = r
				return true
			}
		}
		return false
	}, "first vote never became visible on another node")
	t.Logf("thief sees on node 3 after ONE vote: account=thief prev=%s new=%s votedPower=%d reached=%v voters=%d",
		seen.PreviousAmount, seen.NewAmount, seen.VotedPower, seen.Reached, len(seen.Voters))
	if seen.Reached {
		t.Fatalf("setup: one vote must not reach quorum")
	}

	// It moves everything to a fresh key, in the next block.
	blockID, moved := v3ThiefMovesEverything(t, s, c.urls[3], "1000", 10*time.Second)
	if !moved {
		t.Fatalf("thief could not move its funds")
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, u := range c.urls {
			if ledger.AccountAmount(v3Accounts(u), s.X).Cmp(rat("999.9")) != 0 {
				return false
			}
		}
		return true
	}, "outflow not committed everywhere")
	t.Logf("thief's outflow committed in block %d; T=%s X=%s", blockID,
		ledger.AccountAmount(v3Accounts(c.urls[0]), s.T).RatString(), ledger.AccountAmount(v3Accounts(c.urls[0]), s.X).RatString())

	// The remaining banks keep voting, one at a time, for what they agreed on (1000 -> 0 at seq).
	for _, b := range []int{1, 2, 0} {
		v3Post(c.urls[b]+"/hdls/reassignment-vote", v3SingleVote(s.banks[b], s.T, prev, next, seq))
	}
	time.Sleep(50 * time.Millisecond)
	for _, r := range v3Gov(c.urls[1]).Reassignments {
		if r.Account == s.T {
			t.Logf("after the move, pool on node 1: votedPower=%d reached=%v amendsCurrent=%v", r.VotedPower, r.Reached, r.AmendsCurrent)
		}
	}
	h := v3Height(c.urls[0])
	waitFor(t, 15*time.Second, func() bool { return v3Height(c.urls[0]) >= h+4 }, "chain stalled")
	final := v3Accounts(c.urls[0])
	gov := v3Gov(c.urls[0])
	t.Logf("4 blocks later: seq %d -> %d, X=%s, pooled reassignments=%d",
		seq, gov.GovernanceSequence, ledger.AccountAmount(final, s.X).RatString(), len(gov.Reassignments))
	if gov.GovernanceSequence == seq && ledger.AccountAmount(final, s.X).Cmp(rat("999.9")) == 0 {
		t.Errorf("VGL-N1 CONFIRMED: the seizure was visible after 1 of 3 needed votes; the target moved 999.9 to a fresh key first and the claw-back can never enact")
	}
}

// The suggested mitigation: banks sign off-pool and ONE bank POSTs the complete certificate, once, to
// ONE node. Does today's code pool it, gossip it with all its votes, and enact it?
func TestV3VGLN1OffPoolCompleteCertificateOnePOST(t *testing.T) {
	for target := 0; target < 4; target++ {
		t.Run(fmt.Sprintf("post-to-node-%d", target), func(t *testing.T) {
			s := v3NewSetup()
			c := v3StartHTTPCluster(t, s.banks, s.set, s.genesis, 400*time.Millisecond)
			defer c.stop()
			waitFor(t, 10*time.Second, func() bool { return v3Height(c.urls[0]) >= 3 }, "network did not start")
			seq := v3Gov(c.urls[0]).GovernanceSequence
			prev, next := rat("1000"), rat("0")
			cert := ledger.Reassignment{Account: s.T, PreviousAmount: prev, NewAmount: next, Sequence: seq}
			for _, b := range []int{0, 1, 3} { // any 3 of 4, signed separately, never POSTed individually
				vote, _ := ledger.SignReassignmentVote(s.banks[b], s.T, prev, next, seq)
				cert.Votes = append(cert.Votes, vote)
			}
			hPost := v3Height(c.urls[target])
			if code, body := v3Post(c.urls[target]+"/hdls/reassignment-vote", cert); code != http.StatusAccepted {
				t.Fatalf("complete cert POST: %d %s", code, body)
			}
			other := (target + 2) % 4
			sawOnOther := false
			waitFor(t, 15*time.Second, func() bool {
				if !sawOnOther {
					for _, r := range v3Gov(c.urls[other]).Reassignments {
						if r.Account == s.T && r.Reached && len(r.Voters) == 3 {
							sawOnOther = true
						}
					}
				}
				for _, u := range c.urls {
					var g GovernanceView
					if v3Get(u+"/hdls/governance", &g) != nil || g.GovernanceSequence != seq+1 {
						return false
					}
				}
				return true
			}, "the complete certificate did not enact on every node")
			acc := v3Accounts(c.urls[other])
			var name string
			for _, a := range acc {
				if a.PublicKey == s.T {
					name = a.Name
				}
			}
			t.Logf("POSTed once to node %d at height %d; enacted everywhere by height %d; node %d saw full cert via gossip=%v; T=%s (name %q) X=%s",
				target, hPost, v3Height(c.urls[0]), other, sawOnOther, ledger.AccountAmount(acc, s.T).RatString(), name, ledger.AccountAmount(acc, s.X).RatString())
		})
	}
}

// Same complete certificate, but the target watches and races it into the same block (today's code,
// no C12 exclusion rule): does off-pool collection alone stop a watching target?
func TestV3VGLN1OffPoolCertTargetRacesSameBlock(t *testing.T) {
	escaped := 0
	const runs = 3
	for run := 0; run < runs; run++ {
		s := v3NewSetup()
		c := v3StartHTTPCluster(t, s.banks, s.set, s.genesis, 400*time.Millisecond)
		waitFor(t, 10*time.Second, func() bool { return v3Height(c.urls[0]) >= 3 }, "network did not start")
		// POST right after a commit, i.e. inside the inter-block window (the realistic common case:
		// with a 1 s window the chain spends almost all its wall time there).
		h0 := v3Height(c.urls[0])
		waitFor(t, 10*time.Second, func() bool { return v3Height(c.urls[0]) > h0 }, "no commit")
		seq := v3Gov(c.urls[0]).GovernanceSequence
		prev, next := rat("1000"), rat("0")
		cert := ledger.Reassignment{Account: s.T, PreviousAmount: prev, NewAmount: next, Sequence: seq}
		for _, b := range []int{0, 1, 2} {
			vote, _ := ledger.SignReassignmentVote(s.banks[b], s.T, prev, next, seq)
			cert.Votes = append(cert.Votes, vote)
		}
		v3Post(c.urls[0]+"/hdls/reassignment-vote", cert)
		waitFor(t, 2*time.Second, func() bool {
			for _, r := range v3Gov(c.urls[3]).Reassignments {
				if r.Account == s.T {
					return true
				}
			}
			return false
		}, "cert not visible")
		h := v3Height(c.urls[3])
		pay, _ := ledger.SignPayment(s.thief, ledger.Payment{PublicKeyFrom: s.T, PublicKeyTo: s.X, Amount: rat("1000"), BlockID: h})
		code, _ := v3Post(c.urls[3]+"/hdls/pay", pay)
		waitFor(t, 15*time.Second, func() bool { return v3Gov(c.urls[0]).GovernanceSequence == seq+1 }, "cert did not enact")
		time.Sleep(100 * time.Millisecond)
		x := ledger.AccountAmount(v3Accounts(c.urls[0]), s.X)
		t.Logf("run %d: pay status %d for block %d; after enactment X=%s", run, code, h, x.RatString())
		if x.Sign() > 0 {
			escaped++
		}
		c.stop()
	}
	t.Logf("target escaped a complete off-pool certificate in the same block %d/%d times (C12 path)", escaped, runs)
}
