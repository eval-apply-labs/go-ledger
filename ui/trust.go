package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The wallet believes its node's state only when validators it trusts signed it. Their names, keys and
// powers are pinned in <keyfile>.trust, with the network's id (spec 7.2): the list a network's
// operator publishes, or, when that file is absent, the id the node serves and the validators that
// signed the first state it served (trust on first use). The wallet signs and verifies for the pinned
// id, so its signatures count on that network only. Every state the wallet shows must carry a commit
// in which pinned names holding more than ⅔ of the pinned power signed the block that produced it,
// each under its pinned key. So neither a dishonest node nor anyone between the wallet and its node
// can show a balance the network never committed, or list itself as a validator to vouch for a
// receipt. The pin follows the chain (Q17): when such a state carries another validator set, and its
// commit is a quorum of that set too, the wallet pins that set in place of the old one. So the pin
// moves only to a set that more than ⅔ of the pinned power signed for, never by the looser rule of a
// node's catch-up, where more than ⅓ suffices: a pin may hold keys that leaked, often why a bank was
// re-keyed. The network id never moves. If banks holding a third or more of the pinned power left or re-keyed since
// the wallet last read a state, no state verifies, and a new list must replace the file (see
// errBanksChanged). The wallet reads the file only at start (LoadOrCreateWallet). A checkpoint someone
// carried in on a file, when the node can't be reached, is believed by the same rules
// (LoadCarriedCheckpoint).

// walletPin is what <keyfile>.trust holds (spec 7.2): the network id and the validators, each by name,
// key and power.
type walletPin struct {
	NetworkID  string                `json:"networkId"`
	Validators []consensus.Validator `json:"validators"`
}

// loadPin reads the pinned network id and validators, or returns none if nothing is pinned yet. The id
// must be one of the network named networkName: the name followed by "/". A pin without an id belongs to
// an earlier network: Phase 1 pinned validators only, as a list.
func loadPin(path, networkName string) (string, *consensus.ValidatorSet, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	var pin walletPin
	if err == nil && (json.Unmarshal(data, &pin) != nil || pin.NetworkID == "") {
		err = errors.New("the file belongs to an earlier network; delete it to pin on first use, or put your operator's list there")
	}
	if err == nil && !strings.HasPrefix(pin.NetworkID, networkName+"/") {
		err = fmt.Errorf("it pins the network %q, not one named %q", pin.NetworkID, networkName)
	}
	for _, validator := range pin.Validators {
		if err == nil && !ledger.CanonicalName(validator.Name) {
			err = fmt.Errorf("the validator name %q is not a name", validator.Name)
		}
	}
	pinned := consensus.NewValidatorSet(pin.Validators)
	if err == nil && pinned.TotalPower() == 0 {
		err = errors.New("no validator with power")
	}
	if err != nil {
		return "", nil, fmt.Errorf("cannot read the validators pinned in %s: %w", path, err)
	}
	return pin.NetworkID, pinned, nil
}

// savePin writes the network id and the validators trusted on first use. It never replaces a file, so a
// list an operator put in place while the wallet ran is not overwritten.
func savePin(path, networkID string, validators []consensus.Validator) error {
	data, err := json.Marshal(walletPin{NetworkID: networkID, Validators: validators})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// replacePin writes the moved pin (movePin) to a new file and renames it over the old one, so a crash
// leaves the old pin or the new one, never half of one.
func replacePin(path, networkID string, validators []consensus.Validator) error {
	data, err := json.Marshal(walletPin{NetworkID: networkID, Validators: validators})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

// errBanksChanged is why no state verifies when the node's commit, or a carried file's, is a quorum of
// the set its checkpoint carries, but the pinned names among its signers hold no more than ⅔ of the
// pinned power: banks holding a third or more of it left or re-keyed since the wallet last read a
// state, too many at once for the pin to follow. (A node or a file that makes up a set of its own and
// signs for it looks the same, so any node or file, or anyone on the plain-HTTP path, can make the
// wallet say this. The message, which names the source, therefore says to take a new list, or pin
// again, only at a bank.) The wallet then takes a new list only as at the start: from the file, or on
// first use at a bank.
var errBanksChanged = errors.New("the banks that sign have changed since this wallet last checked")

// verifiedState is the node's committed checkpoint (spec 4.14), accepted only as described above. It
// refuses an answer with a status of 300 or more, and reads at most node.MaxCheckpointBytes of it, as a
// node does when it fetches a checkpoint. First its shape and hashes (node.Checkpoint.Consistent): its
// state's accounts must be in name order, since the digest hashes them in that order and the wallet
// looks them up by name; the block hash recomputed from its header and its state's digest must be the
// one its commit decided; and the validators it carries must hash to the header's. Then the
// signatures: at first use, a quorum of those validators, for the network id the node serves, which
// must be of the network the wallet was started for; the wallet then pins the id and the validators.
// Later, a quorum of the pinned validators, for the pinned id; when it is not one, but a quorum of the
// set the checkpoint carries, the refusal is errBanksChanged. Last, its height: the wallet refuses a
// state older than the newest it verified (spec 7.2), so its node cannot roll it back to before a
// pending change. The wallet then moves the pin to the set the state carries, if that is another set
// and its commit is a quorum of it too (movePin), and keeps the height and the name the state gives its
// key (walletState). A page reads it once, and shows and acts on that one state.
//
// When the node's state is refused, the newest state the wallet verified may stand in for it
// (newestVerifiedState).
func (wallet *Wallet) verifiedState() (node.Checkpoint, error) {
	checkpoint, _, err := wallet.newestVerifiedState()
	return checkpoint, err
}

// newestVerifiedState is verifiedState, and says whether the state it gives is the node's or a newer
// one the wallet verified before, which stands in for the node's. The wallet keeps the newest state it
// verified in memory (newestCheckpoint), whether from its node or from a file the user loaded
// (LoadCarriedCheckpoint, spec §7.8). That state stands in only when the node serves a state its
// validators signed, but older than that one (as a CDN's cache servers may hold copies of different
// ages, spec §6.3), or, when it came from a file, when the node's checkpoint can't be read at all: no
// answer, or an HTTP error status. Any other refusal, a state the pin does not vouch for or one that
// does not match its commit, is given as it is, so no payment or update goes to that node (a freeze
// still does: Freeze). keptFrom is "" when the state is the node's, and otherwise the source of the
// state that stands in: "node" or "file". While a file stands in, no payment or update goes to the node
// either (Send, UpdateAccount): a payment is saved as text instead. A state below the newest height the
// wallet verified never stands in, so an older state is never shown as current. Once the node's state
// verifies again, it is the newest, and the file is forgotten.
func (wallet *Wallet) newestVerifiedState() (checkpoint node.Checkpoint, keptFrom string, err error) {
	checkpoint, reached, err := wallet.nodeCheckpoint()
	if err == nil {
		checkpoint, err = wallet.verifyUnderPin(checkpoint, "node")
	}
	if err == nil {
		return checkpoint, "", nil
	}
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	kept := wallet.newestCheckpoint
	if kept != nil && kept.State.Height >= wallet.saved.NewestHeight &&
		(errors.Is(err, errOlderState) || (!reached && wallet.newestSource == "file")) {
		return *kept, wallet.newestSource, nil
	}
	return node.Checkpoint{}, "", err
}

// nodeCheckpoint is the checkpoint the node serves at /hdls/checkpoint, not yet verified. reached is
// false when the request got no answer, or an HTTP error status: the case a checkpoint from a file
// stands in for (newestVerifiedState).
func (wallet *Wallet) nodeCheckpoint() (checkpoint node.Checkpoint, reached bool, err error) {
	response, err := wallet.client.Get(wallet.nodeURL + "/hdls/checkpoint")
	if err != nil {
		return node.Checkpoint{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return node.Checkpoint{}, false, fmt.Errorf("the node answered its checkpoint with %s", response.Status)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, node.MaxCheckpointBytes)).Decode(&checkpoint); err != nil {
		return node.Checkpoint{}, true, err
	}
	return checkpoint, true, nil
}

// errOlderState is why verifyUnderPin refuses a state its validators signed: it is older than the
// newest state the wallet verified (spec 7.2). The newest state the wallet kept may then stand in
// (newestVerifiedState).
var errOlderState = errors.New("it may be hiding a newer change")

// verifyUnderPin accepts checkpoint, which came from source ("node" or "file"), only as verifiedState
// describes, moves the pin if it carries another set its commit is a quorum of, and keeps its height and
// the name it gives the wallet's key. It keeps the checkpoint itself as the newest state verified, with
// its source, under the same lock as the check of its height, so a state older than one another page
// verified meanwhile is never kept.
func (wallet *Wallet) verifyUnderPin(checkpoint node.Checkpoint, source string) (node.Checkpoint, error) {
	if !checkpoint.Consistent() {
		return node.Checkpoint{}, fmt.Errorf("the %s's state is not in name order, or does not match its commit", source)
	}
	wallet.mutex.Lock()
	firstUse := wallet.pinned == nil
	wallet.mutex.Unlock()
	var servedNetworkID string
	if firstUse { // asked without the mutex held, so a slow node holds up no other page
		var err error
		if servedNetworkID, err = wallet.servedNetworkID(); err != nil {
			return node.Checkpoint{}, err
		}
		if !strings.HasPrefix(servedNetworkID, wallet.networkName+"/") {
			return node.Checkpoint{}, fmt.Errorf("the node serves the network %q, not one named %q", servedNetworkID, wallet.networkName)
		}
	}
	wallet.mutex.Lock()
	defer wallet.mutex.Unlock()
	firstUse = wallet.pinned == nil // another page may have pinned meanwhile
	signers := consensus.NewValidatorSet(checkpoint.Validators)
	switch {
	case firstUse && !checkpoint.Commit.Verify(signing.NetworkScheme(), servedNetworkID, signers):
		return node.Checkpoint{}, errors.New("the node has no commit signed by its validators yet")
	case !firstUse && !signedByPinned(checkpoint.Commit, wallet.pinned, wallet.networkID):
		if checkpoint.Commit.Verify(signing.NetworkScheme(), wallet.networkID, signers) {
			return node.Checkpoint{}, fmt.Errorf("%w, and too few of the banks this wallet trusts signed this %s's state. Get the current list of banks from any bank (the .trust file of the bank's own wallet), save it as %s, and restart the wallet. "+
				"Or switch to another bank: delete %s, and restart the wallet with that bank's node, at the bank; it then trusts the banks that sign that node's state. "+
				"Take a new list, or pin again, only at a bank, never through a %s you do not trust: a %s that is not your bank's can show this message to get its own list trusted. The freeze button still works",
				errBanksChanged, source, wallet.pinPath, wallet.pinPath, source, source)
		}
		return node.Checkpoint{}, fmt.Errorf("its state is not signed by the validators pinned in %s, nor by the validators it lists: this %s does not serve your network's ledger; ask for another %s", wallet.pinPath, source, source)
	case checkpoint.State.Height < wallet.saved.NewestHeight:
		return node.Checkpoint{}, fmt.Errorf("the %s serves the state of block %d, older than the state of block %d this wallet verified before: %w; try again later, or ask for another %s", source, checkpoint.State.Height, wallet.saved.NewestHeight, errOlderState, source)
	}
	switch {
	case firstUse:
		if err := savePin(wallet.pinPath, servedNetworkID, checkpoint.Validators); err != nil {
			return node.Checkpoint{}, fmt.Errorf("cannot pin the node's validators: %w; if you put a list in that file, restart the wallet to read it", err)
		}
		wallet.networkID, wallet.pinned = servedNetworkID, signers
		log.Printf("pinned the network %s and the validators that signed %s's state in %s (first use); to choose them yourself, replace that file with the list your network's operator publishes and restart the wallet", servedNetworkID, wallet.nodeURL, wallet.pinPath)
	case !slices.Equal(checkpoint.Validators, wallet.pinned.Validators()) && checkpoint.Commit.Verify(signing.NetworkScheme(), wallet.networkID, signers):
		wallet.movePin(checkpoint.State.Height, checkpoint.Validators)
	}
	name := nameOfKey(checkpoint.State.Accounts, wallet.publicKey)
	if checkpoint.State.Height > wallet.saved.NewestHeight || (name != "" && name != wallet.saved.LastName) {
		wallet.saved.NewestHeight = checkpoint.State.Height
		if name != "" {
			wallet.saved.LastName = name
		}
		wallet.saveState()
	}
	wallet.newestCheckpoint, wallet.newestSource = &checkpoint, source
	return checkpoint, nil
}

// LoadCarriedCheckpoint takes a checkpoint file someone carried in when the node can't be reached
// (spec §7.8): a bank's public hdls/checkpoint, as text. It is verified under the pin exactly as the
// node's checkpoint is (verifyUnderPin): pinned names holding more than ⅔ of the pinned power must
// have signed it, it must not be older than the newest state the wallet verified, and it moves the pin
// as the node's would. So a forged file, or another network's, is refused with the reason. The wallet
// needs a pin first: a file carries no network id to pin on first use. Once verified, it is the newest
// state the wallet verified, and the pages show its state, and a payment saved as text names its block
// from its height (SavePayment), while the node's checkpoint can't be read or its state is older
// (newestVerifiedState). It is kept in memory only: a restart asks the node again. A text longer than a
// node takes (node.MaxCheckpointBytes) is refused before it is decoded: the form's bound is three times
// that, room for a file's url-encoding, so a text that needs no encoding passes it at up to three times.
func (wallet *Wallet) LoadCarriedCheckpoint(text string) (node.Checkpoint, error) {
	if wallet.pinnedNetworkID() == "" {
		return node.Checkpoint{}, fmt.Errorf("this wallet trusts no banks yet: open its page once at your bank, or put your network's list of banks in %s and restart the wallet", wallet.pinPath)
	}
	if len(text) > node.MaxCheckpointBytes {
		return node.Checkpoint{}, errCheckpointTooLarge
	}
	var checkpoint node.Checkpoint
	if err := json.Unmarshal([]byte(text), &checkpoint); err != nil {
		return node.Checkpoint{}, fmt.Errorf("unreadable checkpoint: paste the whole file (%v)", err)
	}
	return wallet.verifyUnderPin(checkpoint, "file")
}

// errCheckpointTooLarge is why a checkpoint file longer than a node takes is refused: in the form that
// carries it (loadCheckpoint), or once taken out of the form (LoadCarriedCheckpoint).
var errCheckpointTooLarge = fmt.Errorf("it is too large: a checkpoint file has at most %d MiB", node.MaxCheckpointBytes>>20)

// movePin pins validators, the set a state the pin vouches for carries, in place of the pinned set
// (Q17), and writes it to <keyfile>.trust. If that file no longer holds the pin the wallet holds, an
// operator put a list there while the wallet ran: the file is kept, and the wallet reads it when
// restarted. The network id stays. The caller holds the mutex.
func (wallet *Wallet) movePin(height int64, validators []consensus.Validator) {
	networkIDOnDisk, pinnedOnDisk, err := loadPin(wallet.pinPath, wallet.networkName)
	fileHoldsThePin := err == nil && networkIDOnDisk == wallet.networkID && pinnedOnDisk != nil && slices.Equal(pinnedOnDisk.Validators(), wallet.pinned.Validators())
	wallet.pinned = consensus.NewValidatorSet(validators)
	switch {
	case !fileHoldsThePin:
		log.Printf("the validators changed at block %d; the wallet follows them, but keeps %s, which no longer holds its pin: it reads that file when restarted", height, wallet.pinPath)
	case replacePin(wallet.pinPath, wallet.networkID, validators) != nil:
		log.Printf("the validators changed at block %d; the wallet follows them, but cannot write them to %s", height, wallet.pinPath)
	default:
		log.Printf("the validators changed at block %d: the pin in %s now holds the %d validators of that block, which more than ⅔ of the pinned power signed for", height, wallet.pinPath, len(validators))
	}
}

// servedNetworkID is the network id the node serves on /hdls/status.
func (wallet *Wallet) servedNetworkID() (string, error) {
	_, networkID, err := wallet.status()
	return networkID, err
}

// signedByPinned reports whether pinned names holding more than ⅔ of the pinned power signed commit,
// each under its pinned key. Precommits from other names, and from pinned names under another key, are
// left out rather than refused: validators who joined or re-keyed since the pin do not break it, and
// none of them counts toward the quorum.
func signedByPinned(commit consensus.Commit, pinned *consensus.ValidatorSet, network string) bool {
	votes := commit.Votes
	commit.Votes = nil
	for _, vote := range votes {
		if pinned.Contains(vote.Validator) && vote.Verify(signing.NetworkScheme(), network, pinned.PublicKey(vote.Validator)) {
			commit.Votes = append(commit.Votes, vote)
		}
	}
	return commit.Verify(signing.NetworkScheme(), network, pinned)
}
