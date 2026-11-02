package node

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The slow-down switch (spec §7.6) lengthens the round timers, and the wait of a node back without its
// sign log, only. armTimeout waits the larger of the governed step timeout and the switch, times the
// round plus one. Re-gossip keeps the governed step timeout as its interval. The wait is twice the larger
// of the two, so that in pigeon mode a carry brings the node's earlier votes back first (review B2).
// Here the governed step timeout is 50 ms: with the switch at 200 ms the timer of round 1 fires no
// sooner than 400 ms after it is armed, while the re-gossip loop ticks every 50 ms meanwhile.
func TestSlowDownSwitchLengthensOnlyTheRoundTimers(t *testing.T) {
	genesis := testGenesis([]ledger.Account{testAccount("validator", "100")}, 50*time.Millisecond, 0)
	newNode := func(minimumStepTimeout time.Duration) *Node {
		config := testConfig(staticChain("validator"), "validator", genesis)
		config.MinimumStepTimeout = minimumStepTimeout
		return New(config, &recordingTransport{}) // no loop runs: the test reads the events the timers send
	}
	for _, testCase := range []struct{ minimum, want time.Duration }{
		{0, 50 * time.Millisecond}, {20 * time.Millisecond, 50 * time.Millisecond}, {200 * time.Millisecond, 200 * time.Millisecond},
	} {
		node := newNode(testCase.minimum)
		if node.stepTimeout() != testCase.want || node.reGossipInterval() != 50*time.Millisecond || node.ownSignaturesWait() != 2*testCase.want {
			t.Errorf("with the switch at %v the round timers' base is %v, the re-gossip interval %v and the wait without a sign log %v, want %v, the governed 50ms and %v",
				testCase.minimum, node.stepTimeout(), node.reGossipInterval(), node.ownSignaturesWait(), testCase.want, 2*testCase.want)
		}
	}

	node := newNode(200 * time.Millisecond)
	defer node.Stop()
	go node.reGossipLoop()
	armed := time.Now()
	node.armTimeout(consensus.StepPropose, 1)
	reGossips := 0
	for {
		select {
		case incoming := <-node.events:
			if incoming.kind == eventRegossip {
				reGossips++
				continue
			}
			if elapsed := time.Since(armed); incoming.kind != eventTimeout || elapsed < 400*time.Millisecond {
				t.Fatalf("the round 1 timer fired after %v, want 400ms or more: the switch's 200ms times 2", elapsed)
			}
			if reGossips < 3 {
				t.Errorf("the re-gossip loop ticked %d times while the round timer ran, want it every 50ms, the governed interval", reGossips)
			}
			return
		case <-time.After(5 * time.Second * testTimeSlack):
			t.Fatal("the round timer never fired")
		}
	}
}

// No rule reads the clock (spec §7.1): no file of ledger or consensus but a test imports time. A block
// carries no timestamp, and every deadline counts in blocks. The slow-down switch is safe because of it
// (spec §7.6): a longer timer only makes a node wait longer before it votes "no block".
func TestNoRuleReadsTheClock(t *testing.T) {
	for _, folder := range []string{"../ledger", "../consensus"} {
		paths, err := filepath.Glob(filepath.Join(folder, "*.go"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("premise: the folder %s holds Go files (error %v)", folder, err)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imported := range file.Imports {
				if importPath, _ := strconv.Unquote(imported.Path.Value); importPath == "time" {
					t.Errorf("%s imports time: no rule may read the clock", path)
				}
			}
		}
	}
}
