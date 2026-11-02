package main

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
)

// Once its node's state verifies, the main page asks the other banks how far the chain has gone
// (spec 7.2, Q25). A bank that colludes with a thief can hide a pending key change from its customer:
// its node keeps serving the last genuine state from before the change was filed, which verifies and
// is not older than the newest the wallet saw. The other banks' nodes report the newer blocks. The
// wallet asks each validator of the verified state for /hdls/status, at the public address its account
// publishes, and counts an answer only when it names the pinned network id. A validator with no public
// address counts as one that does not answer.
//
// Their heights are not signed, and that is enough. They choose which note the page shows, never the
// state it shows, which the pinned ⅔ signed: a lying node cannot hide anything that state holds. Each
// note needs banks holding more than ⅓ of the power. Lying banks raise a false "behind" only if they
// hold more than ⅓. They make a node that hides blocks look "aligned" only if they hold more than ⅓
// too: more than ⅔ answered, so without them the honest banks that answered hold more than ⅓, and
// report the newer blocks. Banks holding a third of the power or less change no note by lying.

// otherBanksTimeout bounds how long the page waits for the other banks. A node answers /hdls/status
// from its committed view, off its event loop, in under 200 bytes: one round trip, milliseconds on a
// community network and well under a second across the world. The wallet asks every bank at once, so
// a page waits this long at most, and only when a bank's node does not answer at all (a refused
// connection fails at once). Nothing is cached: a page reads one state and asks once.
const otherBanksTimeout = 2 * time.Second

// behindTolerance is how many blocks past the verified state the other banks may report before the
// wallet warns that its node may be hiding newer blocks. The network moves while the wallet reads its
// node's checkpoint (at most 5 seconds, its client's timeout) and asks the others (at most
// otherBanksTimeout), and nodes commit a block a moment apart: about 8 blocks at the default of one
// block a second. A hidden change keeps the chain committing a block each BlockIntervalMs until it
// falls due, about a day later (spec 6.6), so the warning comes some 10 blocks after it is filed.
const behindTolerance = 10

// chainStallAfter is how long the network's newest block may stay the same before the wallet warns
// that the chain may have stalled, under parameters, the verified state's: twice the longest an idle
// chain waits between two blocks. After a commit a node waits BlockIntervalMs (the post-commit window,
// which the ⅔ may raise to a day), then node.IdleHeartbeat with no work, so an idle chain commits a
// block about every IdleHeartbeat + BlockIntervalMs: about every 5 minutes at the default of a second.
func chainStallAfter(parameters ledger.LedgerParameters) time.Duration {
	return 2 * (node.IdleHeartbeat + time.Duration(parameters.BlockIntervalMs)*time.Millisecond)
}

// bankHeight is one bank's answer: its name, its power, and the committed height its node reports.
type bankHeight struct {
	name   string
	power  int64
	height int64
}

// bankCheck is what the main page says once the wallet asked the other banks (checkOtherBanks). Each
// field is a sentence, or "" when it does not apply.
type bankCheck struct {
	Behind    string // banks holding more than ⅓ of the power report blocks past the verified state
	Unchecked string // the banks that answered hold no more than ⅔ of the power
	Aligned   string // more than ⅔ answered, and the verified state is as current as theirs
	Stalled   string // the network's newest block has not changed for chainStallAfter
}

// checkOtherBanks asks the node of each validator of checkpoint, the verified state, for its height,
// all at once, and says what the answers show (see the top of this file). The requests belong to the
// page: they end when pageContext does, once the browser leaves the page, or after otherBanksTimeout.
func (wallet *Wallet) checkOtherBanks(pageContext context.Context, checkpoint node.Checkpoint) bankCheck {
	networkID := wallet.pinnedNetworkID()
	client := node.NewHTTPClient(otherBanksTimeout)           // follows no redirect
	answers := make([]bankHeight, len(checkpoint.Validators)) // left at zero power: no answer
	var waitGroup sync.WaitGroup
	for i, validator := range checkpoint.Validators {
		account, _ := ledger.FindAccount(checkpoint.State.Accounts, validator.Name)
		statusURL := statusURLAt(account.PublicAddress)
		if statusURL == "" {
			continue // its bank publishes no public address: it does not answer
		}
		waitGroup.Go(func() {
			heightInProgress, servedNetworkID, err := readStatus(pageContext, client, statusURL)
			if err == nil && servedNetworkID == networkID {
				answers[i] = bankHeight{name: validator.Name, power: validator.Power, height: heightInProgress - 1}
			}
		})
	}
	waitGroup.Wait()

	validators := consensus.NewValidatorSet(checkpoint.Validators)
	verifiedHeight, totalPower := checkpoint.State.Height, validators.TotalPower()
	var answeredPower, aheadPower int64
	var heights []string
	for _, answer := range answers {
		if answer.power == 0 {
			continue
		}
		answeredPower += answer.power
		if answer.height > verifiedHeight+behindTolerance {
			aheadPower += answer.power
		}
		heights = append(heights, fmt.Sprintf("%s %d", answer.name, answer.height))
	}
	var check bankCheck
	if aheadPower >= validators.SkipThreshold() {
		check.Behind = fmt.Sprintf("Your node may be hiding newer blocks: it shows block %d, but banks holding %d%% of the power report newer ones (each bank's newest block: %s). "+
			"A change on your name may be waiting that this page cannot show. Check your name at another bank.", verifiedHeight, aheadPower*100/totalPower, strings.Join(heights, ", "))
	}
	switch {
	case answeredPower < validators.Quorum():
		check.Unchecked = fmt.Sprintf("This wallet could only check banks holding %d%% of the power: too few to tell whether your node shows the newest blocks.", answeredPower*100/totalPower)
	case check.Behind == "":
		check.Aligned = fmt.Sprintf("Checked with banks holding %d%% of the power: this state is as current as theirs.", answeredPower*100/totalPower)
	}

	networkHeight := max(verifiedHeight, heightOfAThird(answers, validators.SkipThreshold()))
	wallet.mutex.Lock()
	if networkHeight > wallet.chainHeight || wallet.chainHeightSince.IsZero() {
		wallet.chainHeight, wallet.chainHeightSince = networkHeight, time.Now()
	}
	unchangedFor := time.Since(wallet.chainHeightSince)
	wallet.mutex.Unlock()
	if unchangedFor > chainStallAfter(checkpoint.State.Parameters) {
		check.Stalled = fmt.Sprintf("No new block for %d minutes (the newest is block %d): the chain may have stalled, or your node is stuck. Payments and changes wait until it moves again.",
			int(unchangedFor.Minutes()), networkHeight)
	}
	return check
}

// statusURLAt is the /hdls/status URL of the node at address, a validator's public address, built
// from the address's scheme and host alone, never by appending to the text: nothing else in the address,
// a path or a query, can turn the question into another request, such as one for the wallet's own page.
// It is "" for no address, and for one not in the plain form the ledger takes (ledger.CanonicalAddress),
// which a state the pinned ⅔ signed never holds.
func statusURLAt(address string) string {
	parsed, err := url.Parse(address)
	if err != nil || !ledger.CanonicalAddress(address) {
		return ""
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/hdls/status"}).String()
}

// heightOfAThird is the newest committed height that banks holding at least threshold of the power
// (more than ⅓) all report reaching, or 0 when the banks that answered hold less. Banks holding ⅓ or
// less cannot raise it by lying. The wallet takes it as the network's newest height.
func heightOfAThird(answers []bankHeight, threshold int64) int64 {
	newestFirst := slices.SortedFunc(slices.Values(answers), func(first, second bankHeight) int { return cmp.Compare(second.height, first.height) })
	var power int64
	for _, answer := range newestFirst {
		if power += answer.power; power >= threshold {
			return answer.height
		}
	}
	return 0
}
