package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
	"github.com/eval-apply-labs/go-ledger/node"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The wallet believes its node's state only when validators it trusts signed it. Their names, keys and
// powers are pinned in <keyfile>.trust, with the network's id (spec 7.2): the list a network's
// operator publishes, or, when that file is absent, the id the node serves and the validators that
// signed the first state it served, kept from then on (trust on first use). The wallet signs and
// verifies for the pinned id, so its signatures count on that network only. Every state the wallet
// shows must carry a commit in which pinned names holding more than ⅔ of the pinned power signed the
// block that produced it, each under its pinned key. So neither a dishonest node nor anyone between the
// wallet and its node can show a balance the network never committed, or list itself as a validator to
// vouch for a receipt. On a network whose validators change with stake, or whose banks re-key, the pin
// holds until validators with a third of its power have left or re-keyed; then the operator's new list
// replaces the file. The wallet reads the file only at start (LoadOrCreateWallet), so it takes a new
// list once restarted.

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

// verifiedState is the node's committed checkpoint (spec 4.14), accepted only as described above. It
// refuses an answer with a status of 300 or more, and reads at most node.MaxCheckpointBytes of it, as a
// node does when it fetches a checkpoint. First its shape and hashes (node.Checkpoint.Consistent): its
// state's accounts must be in name order, since the digest hashes them in that order and the wallet
// looks them up by name; the block hash recomputed from its header and its state's digest must be the
// one its commit decided; and the validators it carries must hash to the header's. Then the
// signatures: at first use, a quorum of those validators, for the network id the node serves, which
// must be of the network the wallet was started for; the wallet then pins the id and the validators.
// Later, a quorum of the pinned validators, for the pinned id. Last, its height: the wallet refuses a
// state older than the newest it verified (spec 7.2), so its node cannot roll it back to before a
// pending change. The wallet then keeps that height, and the name the state gives its key
// (walletState). A page reads it once, and shows and acts on that one state.
func (wallet *Wallet) verifiedState() (node.Checkpoint, error) {
	var checkpoint node.Checkpoint
	response, err := wallet.client.Get(wallet.nodeURL + "/hdls/checkpoint")
	if err != nil {
		return node.Checkpoint{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return node.Checkpoint{}, fmt.Errorf("the node answered its checkpoint with %s", response.Status)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, node.MaxCheckpointBytes)).Decode(&checkpoint); err != nil {
		return node.Checkpoint{}, err
	}
	if !checkpoint.Consistent() {
		return node.Checkpoint{}, errors.New("the node's state is not in name order, or does not match its commit")
	}
	wallet.mutex.Lock()
	firstUse := wallet.pinned == nil
	wallet.mutex.Unlock()
	var servedNetworkID string
	if firstUse { // asked without the mutex held, so a slow node holds up no other page
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
	case firstUse && !checkpoint.Commit.Verify(signing.MLDSA65{}, servedNetworkID, signers):
		return node.Checkpoint{}, errors.New("the node has no commit signed by its validators yet")
	case !firstUse && !signedByPinned(checkpoint.Commit, wallet.pinned, wallet.networkID):
		return node.Checkpoint{}, fmt.Errorf("its state is not signed by the validators pinned in %s; if your network's validators changed, replace that file with your operator's list and restart the wallet", wallet.pinPath)
	case checkpoint.State.Height < wallet.saved.NewestHeight:
		return node.Checkpoint{}, fmt.Errorf("the node serves the state of block %d, older than the state of block %d this wallet verified before: it may be hiding a newer change; try again later, or ask another node", checkpoint.State.Height, wallet.saved.NewestHeight)
	}
	if firstUse {
		if err := savePin(wallet.pinPath, servedNetworkID, checkpoint.Validators); err != nil {
			return node.Checkpoint{}, fmt.Errorf("cannot pin the node's validators: %w; if you put a list in that file, restart the wallet to read it", err)
		}
		wallet.networkID, wallet.pinned = servedNetworkID, signers
		log.Printf("pinned the network %s and the validators that signed %s's state in %s (first use); to choose them yourself, replace that file with the list your network's operator publishes and restart the wallet", servedNetworkID, wallet.nodeURL, wallet.pinPath)
	}
	name := nameOfKey(checkpoint.State.Accounts, wallet.publicKey)
	if checkpoint.State.Height > wallet.saved.NewestHeight || (name != "" && name != wallet.saved.LastName) {
		wallet.saved.NewestHeight = checkpoint.State.Height
		if name != "" {
			wallet.saved.LastName = name
		}
		wallet.saveState()
	}
	return checkpoint, nil
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
		if pinned.Contains(vote.Validator) && vote.Verify(signing.MLDSA65{}, network, pinned.PublicKey(vote.Validator)) {
			commit.Votes = append(commit.Votes, vote)
		}
	}
	return commit.Verify(signing.MLDSA65{}, network, pinned)
}
