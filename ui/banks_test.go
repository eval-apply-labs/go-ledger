package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// bankNode is another bank's node as checkOtherBanks reads it: its /hdls/status answers what answer
// gives.
func bankNode(t *testing.T, answer func() any) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(responseWriter).Encode(answer())
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// statusAt is a status for the committed height, of the network networkID.
func statusAt(committedHeight int64, networkID string) func() any {
	return func() any { return map[string]any{"heightInProgress": committedHeight + 1, "networkId": networkID} }
}

// threeBanks is a wallet named alice on a network of three banks of power 1: the first runs the
// wallet's node, at the state of block 9, and publishes its address; the other two publish the public
// addresses given ("" for none). Every bank also publishes an IP address, which the wallet never reads.
func threeBanks(t *testing.T, otherAddresses ...string) (*Wallet, *governanceNode) {
	t.Helper()
	testNode := newGovernanceNode(t)
	wallet := newTestWallet(t, testNode.baseURL)
	banks := []*signing.Signer{testNodeSigner(t), testNodeSigner(t), testNodeSigner(t)}
	accounts := []ledger.Account{{Name: "alice", PublicKey: wallet.PublicKey(), Amount: big.NewRat(5, 1)}}
	for i, address := range append([]string{testNode.baseURL}, otherAddresses...) {
		account := accountOf(banks[i], "0")
		account.PublicAddress, account.IPAddresses = address, []string{"127.0.0.1"}
		accounts = append(accounts, account)
	}
	testNode.setState(banks, accounts...)
	return wallet, testNode
}

// After its node's state verifies at block 9, the main page asks each bank, at the public address its
// account publishes, for its height (Q25), and counts only answers for the pinned network id, read up
// to maxStatusBytes. A bank with no public address, only IP addresses, does not answer. "Behind": banks holding more than ⅓ of the power report blocks more than
// behindTolerance past 9; then it says the node may be hiding newer blocks, with their heights and
// their share of the power. "Unchecked": the banks that answered hold no more than ⅔. "Aligned": more
// than ⅔ answered, and none of that.
func TestWalletChecksTheOtherBanks(t *testing.T) {
	const (
		behind    = "Your node may be hiding newer blocks: it shows block 9, but banks holding "
		unchecked = "This wallet could only check banks holding "
		aligned   = "Checked with banks holding "
	)
	otherNetworkID := ledger.NetworkID("testnet", [32]byte{2})
	for _, testCase := range []struct {
		name          string
		second, third func() any // the other two banks' answers; nil: the bank publishes no public address
		want          []string
	}{
		{"every bank at block 9", statusAt(9, testNetworkID), statusAt(9, testNetworkID), []string{aligned + "100% of the power: this state is as current as theirs."}},
		{"two banks within the tolerance", statusAt(9+behindTolerance, testNetworkID), statusAt(9+behindTolerance, testNetworkID), []string{aligned + "100%"}},
		{"two banks past it", statusAt(10+behindTolerance, testNetworkID), statusAt(60, testNetworkID),
			[]string{behind + "66% of the power report newer ones", "A change on your name may be waiting"}},
		{"one bank of three far past it", statusAt(9, testNetworkID), statusAt(500, testNetworkID), []string{aligned + "100%"}},
		{"one bank publishes no public address", statusAt(9, testNetworkID), nil, []string{unchecked + "66% of the power"}},
		{"one bank serves another network", statusAt(9, testNetworkID), statusAt(500, otherNetworkID), []string{unchecked + "66% of the power"}},
		{"two banks serve another network", statusAt(500, otherNetworkID), statusAt(500, otherNetworkID), []string{unchecked + "33% of the power"}},
		{"one bank answers past the bound", statusAt(9, testNetworkID),
			func() any {
				return map[string]any{"heightInProgress": 10, "networkId": testNetworkID, "padding": strings.Repeat("x", maxStatusBytes)}
			}, []string{unchecked + "66%"}},
		{"two banks past it, one for another network", statusAt(500, testNetworkID), statusAt(500, otherNetworkID), []string{unchecked + "66%"}},
	} {
		var addresses []string
		for _, answer := range []func() any{testCase.second, testCase.third} {
			if answer == nil {
				addresses = append(addresses, "")
			} else {
				addresses = append(addresses, bankNode(t, answer))
			}
		}
		wallet, testNode := threeBanks(t, addresses...)
		body := page(t, wallet, "/")
		if testCase.name == "two banks past it" { // the heights, each after its bank's name, in name order
			var heights []string
			for i, height := range []int64{9, 10 + behindTolerance, 60} {
				heights = append(heights, fmt.Sprintf("%s %d", nameOf(testNode.validators[i]), height))
			}
			slices.Sort(heights)
			testCase.want = append(testCase.want, "(each bank's newest block: "+strings.Join(heights, ", ")+")")
		}
		for _, want := range testCase.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the page does not say %q", testCase.name, want)
			}
		}
		for _, note := range []string{behind, unchecked, aligned} {
			if strings.Contains(body, note) && !strings.Contains(strings.Join(testCase.want, " "), note) {
				t.Errorf("%s: the page also says %q", testCase.name, note)
			}
		}
	}
}

// The wallet asks the other banks all at once, and waits for them at most otherBanksTimeout, so banks
// whose nodes never answer slow a page down by that much at most, not by that much each (Q25).
func TestOtherBanksAreAskedAtOnceAndBriefly(t *testing.T) {
	silent := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		<-request.Context().Done() // answers only once the wallet gives up
	}))
	defer silent.Close()
	wallet, _ := threeBanks(t, silent.URL, silent.URL)
	started := time.Now()
	body := page(t, wallet, "/")
	if elapsed := time.Since(started); elapsed > otherBanksTimeout+otherBanksTimeout/2 {
		t.Errorf("the page took %v with two silent banks, want about %v", elapsed, otherBanksTimeout)
	}
	if !strings.Contains(body, "This wallet could only check banks holding 33% of the power") {
		t.Error("the page does not say it could check only its own bank")
	}
}

// The wallet remembers, in memory, when it first saw the network's newest block: the newest block that
// banks holding more than ⅓ of the power reach, or its own verified one if newer. Once that has not
// changed for chainStallAfter, the page says the chain may have stalled (Q25). One bank of three that
// reports ever newer blocks cannot hide that. Once the chain moves, the warning goes.
func TestStalledChainIsReported(t *testing.T) {
	var liar atomic.Int64
	liar.Store(9)
	wallet, testNode := threeBanks(t, bankNode(t, statusAt(9, testNetworkID)), bankNode(t, func() any { return statusAt(liar.Add(1), testNetworkID)() }))
	const stalled = "the chain may have stalled"
	if body := page(t, wallet, "/"); strings.Contains(body, stalled) {
		t.Fatal("the page says the chain stalled on its first look")
	}
	wallet.mutex.Lock()
	wallet.chainHeightSince = time.Now().Add(-chainStallAfter(governanceParameters) - time.Minute)
	wallet.mutex.Unlock()
	if body := page(t, wallet, "/"); !strings.Contains(body, "No new block for 11 minutes (the newest is block 9): "+stalled) {
		t.Errorf("block 9 is the newest for 11 minutes, and the page does not say the chain may have stalled")
	}
	testNode.mutex.Lock()
	testNode.height = 12
	testNode.mutex.Unlock()
	if body := page(t, wallet, "/"); strings.Contains(body, stalled) {
		t.Error("the chain moved to block 12, and the page still says it may have stalled")
	}
}

// The wallet follows no redirect (node.NewHTTPClient), from its own node or from another bank's: an
// answer that is a redirect is a failed answer, and the URL it names is never asked. Otherwise a node,
// or anyone on the plain-HTTP path, could send the wallet's requests to any URL the wallet's machine
// can reach: the wallet's own page, which asks the banks again, or a router on the user's network
// (review follow-up).
func TestWalletFollowsNoRedirect(t *testing.T) {
	var reached atomic.Int64
	target := bankNode(t, func() any { reached.Add(1); return statusAt(9, testNetworkID)() })
	redirecting := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Redirect(responseWriter, request, target+request.URL.Path, http.StatusFound)
	}))
	defer redirecting.Close()

	if _, _, err := newTestWallet(t, redirecting.URL).status(); err == nil {
		t.Error("the wallet's own node answered its status with a redirect, and the wallet took an answer")
	}
	wallet, _ := threeBanks(t, redirecting.URL, "")
	if body := page(t, wallet, "/"); !strings.Contains(body, "This wallet could only check banks holding 33% of the power") {
		t.Error("a bank's node answered with a redirect, and the page counted that bank")
	}
	if reached.Load() != 0 {
		t.Errorf("the URL the redirects name was asked %d times, want never", reached.Load())
	}
}

// A validator that publishes the wallet's own address, alone or with an empty query, or whose node
// redirects to the wallet's page, costs one page view a bounded number of requests to the wallet: the
// page itself, and at most one question per bank. The wallet asks at the address's scheme and host
// alone (statusURLAt), and the main page is "/" alone, so a question to the wallet is not found and
// asks nothing; and it follows no redirect. Before, "http://<wallet>/?" made the question a request for
// the page, which asked again, without end: about 750 requests in 6 s (review follow-up).
func TestNoSelfLoopThroughTheWalletsOwnAddress(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		address func(walletAddress string) string // the second bank's published address
	}{
		{"the wallet's own address", func(walletAddress string) string { return "http://" + walletAddress }},
		{"the wallet's own address and an empty query", func(walletAddress string) string { return "http://" + walletAddress + "/?" }},
		{"a node that redirects to the wallet's page", func(walletAddress string) string {
			return bankNodeRedirectingTo(t, "http://"+walletAddress+"/")
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			walletAddress := listener.Addr().String()
			wallet, testNode := threeBanks(t, testCase.address(walletAddress), "")
			var requests atomic.Int64
			walletServer := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				routes(wallet, walletAddress).ServeHTTP(responseWriter, request)
			})}}
			walletServer.Start()
			defer walletServer.Close()

			response, err := http.Get("http://" + walletAddress + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			time.Sleep(100 * time.Millisecond) // for a request still on its way
			if bound := int64(1 + len(testNode.validators)); requests.Load() > bound {
				t.Errorf("one page view made %d requests to the wallet, want at most %d: the page and one question per bank", requests.Load(), bound)
			}
			if !strings.Contains(string(body), "This wallet could only check banks holding 33% of the power") {
				t.Error("the page counted a bank whose address leads back to the wallet")
			}
		})
	}
}

// bankNodeRedirectingTo is a bank's node that answers every request with a redirect to target.
func bankNodeRedirectingTo(t *testing.T, target string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Redirect(responseWriter, request, target, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// The wallet asks the other banks within the page's request: once the browser leaves the page, its
// questions to the banks end at once, rather than wait out otherBanksTimeout (review follow-up).
func TestOtherBanksAreAskedOnlyWhileThePageIsOpen(t *testing.T) {
	asked, ended := make(chan struct{}, 1), make(chan time.Time, 1)
	silent := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		asked <- struct{}{}
		<-request.Context().Done() // the wallet gave up the question
		ended <- time.Now()
	}))
	defer silent.Close()
	wallet, _ := threeBanks(t, silent.URL, "")
	pageContext, leavePage := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		routes(wallet, "127.0.0.1:8090").ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(pageContext, "GET", "http://127.0.0.1:8090/", nil))
	}()
	<-asked
	leftAt := time.Now()
	leavePage()
	if waited := (<-ended).Sub(leftAt); waited > otherBanksTimeout/2 {
		t.Errorf("the question to a bank went on %v after the page was left, want it to end at once", waited)
	}
	<-served
}

// The stall threshold follows the verified state's BlockIntervalMs: an idle chain waits that window
// after a commit, then the idle heartbeat, so with a window of an hour, a block 11 minutes old is not a
// stall, and one older than twice an hour and 5 minutes is (review follow-up).
func TestStalledChainWaitsForTheBlockInterval(t *testing.T) {
	wallet, testNode := threeBanks(t, "", "")
	checkpoint := signedCheckpointAt(9, testNode.validators, testNode.accounts)
	checkpoint.State.Parameters.BlockIntervalMs = time.Hour.Milliseconds()
	const stalled = "the chain may have stalled"
	for _, testCase := range []struct {
		unchangedFor time.Duration
		want         bool
	}{{11 * time.Minute, false}, {2*(time.Hour+node.IdleHeartbeat) + time.Minute, true}} {
		wallet.mutex.Lock()
		wallet.chainHeight, wallet.chainHeightSince = 9, time.Now().Add(-testCase.unchangedFor)
		wallet.mutex.Unlock()
		if got := strings.Contains(wallet.checkOtherBanks(context.Background(), checkpoint).Stalled, stalled); got != testCase.want {
			t.Errorf("block 9 unchanged for %v with a block interval of an hour: stalled %v, want %v", testCase.unchangedFor, got, testCase.want)
		}
	}
}
