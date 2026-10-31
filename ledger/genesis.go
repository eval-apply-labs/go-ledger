package ledger

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// Genesis is the genesis file (spec 2): every node of a network reads the same one, byte for byte.
// Exactly one of Validators (static mode) and ValidatorThreshold (dynamic mode) is set. ParseGenesis
// reads it and ValidateGenesis checks it.
type Genesis struct {
	NetworkName        string             `json:"networkName"`        // the readable half of the network id; a name
	Parameters         LedgerParameters   `json:"parameters"`         // the genesis parameters, every one given
	Validators         []GenesisValidator `json:"validators"`         // static mode: the seated names and their powers
	ValidatorThreshold string             `json:"validatorThreshold"` // dynamic mode: the stake a name needs to be seated
	Accounts           []GenesisAccount   `json:"accounts"`
}

// GenesisValidator is one seat of a static genesis. It signs with its account's key, and gossips at
// its account's address.
type GenesisValidator struct {
	Name  string `json:"name"`
	Power int64  `json:"power"`
}

// GenesisAccount is one account of the genesis. No genesis account is frozen or has a pending change.
type GenesisAccount struct {
	Name        string `json:"name"`
	PublicKey   string `json:"publicKey"`
	Amount      string `json:"amount"`   // the balance; zero is allowed
	Bank        string `json:"bank"`     // the bank of record: "" for none, or a name the genesis seats
	Delegate    string `json:"delegate"` // the name this balance backs as stake; "" for none
	Description string `json:"description"`
	Address     string `json:"address"` // the gossip URL a validator publishes (peer DNS)
}

// genesisParameterNames are the keys of the genesis parameters. The file must give every one: a
// parameter left out would read as 0, and a message bound of 0 is no bound at all.
// LedgerParameters has no JSON tags: encoding/json fills its fields from these keys as it ignores
// case, ParseGenesis refuses any other spelling, and snapshots and checkpoints write the Go names.
var genesisParameterNames = []string{"transactionCost", "blockIntervalMs", "stepTimeoutMs", "maxPeers", "maxMessageBytes",
	"newKeyFee", "changeDelayBlocks", "retainBlocks", "maxPaymentAmount", "maxAccountBalance"}

// ParseGenesis reads a genesis file. It refuses parameters that leave one out, and any key it does not
// know, so a misspelt field is never read as a field left empty. A malformed amount parameter is
// refused with its name.
func ParseGenesis(data []byte) (Genesis, error) {
	var parameterKeys struct {
		Parameters map[string]json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(data, &parameterKeys); err != nil {
		return Genesis{}, fmt.Errorf("genesis: %w", err)
	}
	for _, name := range genesisParameterNames {
		if _, given := parameterKeys.Parameters[name]; !given {
			return Genesis{}, fmt.Errorf("genesis: parameters: %s is missing; the genesis gives every parameter", name)
		}
	}
	for name := range parameterKeys.Parameters {
		if !slices.Contains(genesisParameterNames, name) {
			return Genesis{}, fmt.Errorf("genesis: parameters: %q is not a parameter; they are %s", name, strings.Join(genesisParameterNames, ", "))
		}
	}
	// The decoder below reports a bad amount without its field, so the amounts are checked here first.
	for _, name := range []string{"transactionCost", "newKeyFee", "maxPaymentAmount", "maxAccountBalance"} {
		var amount string
		err := json.Unmarshal(parameterKeys.Parameters[name], &amount)
		if err == nil {
			_, err = ParseAmount(amount)
		}
		if err != nil {
			return Genesis{}, fmt.Errorf("genesis: parameters: %s: %v", name, err)
		}
	}
	var genesis Genesis
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&genesis); err != nil {
		return Genesis{}, fmt.Errorf("genesis: %w", err)
	}
	return genesis, nil
}

// ValidateGenesis checks a genesis (spec 2.2), and returns the chain it starts and its state: the
// accounts in name order, the parameters, governance sequence 0 and height 0. Each refusal names the
// field to fix. The genesis must be a state the ledger itself could reach: one account per name and
// one name per key, each in its one spelling (a key shared by two names would let its holder sign as
// both), balances within the cap, delegates and banks that exist, banks that are seated, and
// parameters a vote could have set.
func ValidateGenesis(genesis Genesis) (Chain, LedgerState, error) {
	fail := func(format string, arguments ...any) (Chain, LedgerState, error) {
		return Chain{}, LedgerState{}, fmt.Errorf("genesis: "+format, arguments...)
	}
	const nameRule = "1 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or a digit"
	if !CanonicalName(genesis.NetworkName) {
		return fail("networkName %q is not a name: %s", genesis.NetworkName, nameRule)
	}

	accounts := make([]Account, 0, len(genesis.Accounts))
	names, keys := make(map[string]bool), make(map[string]bool)
	for _, account := range genesis.Accounts {
		if !CanonicalName(account.Name) {
			return fail("accounts: name %q is not a name: %s", account.Name, nameRule)
		}
		if names[account.Name] {
			return fail("accounts: the name %q is given twice", account.Name)
		}
		names[account.Name] = true
		// Only the canonical spelling: signatures verify under no other, and keys are compared as
		// strings, so another spelling of a key would pass for a second key.
		if !signing.ValidPublicKeyBase64(account.PublicKey) {
			return fail("accounts: the publicKey of %q is not an ML-DSA-65 key in canonical base64 (%d characters)", account.Name, signing.PublicKeyBase64Length)
		}
		if keys[account.PublicKey] {
			return fail("accounts: the publicKey of %q is another account's too", account.Name)
		}
		keys[account.PublicKey] = true
		amount, err := ParseAmount(account.Amount)
		if err != nil {
			return fail("accounts: the amount %q of %q: %v", account.Amount, account.Name, err)
		}
		accounts = append(accounts, Account{Name: account.Name, PublicKey: account.PublicKey, Amount: amount, Bank: account.Bank,
			Delegate: account.Delegate, Description: account.Description, Address: account.Address})
	}
	slices.SortFunc(accounts, func(first, second Account) int { return strings.Compare(first.Name, second.Name) })
	for _, account := range accounts {
		if account.Delegate != "" && !names[account.Delegate] {
			return fail("accounts: the delegate %q of %q is not an account", account.Delegate, account.Name)
		}
		if account.Bank == account.Name {
			return fail("accounts: the bank of %q is its own name; a name is never its own bank", account.Name)
		}
	}

	var validatorRule ValidatorRule
	switch {
	case len(genesis.Validators) > 0 && genesis.ValidatorThreshold != "":
		return fail("both validators and validatorThreshold are set; a network has one of the two")
	case len(genesis.Validators) > 0:
		listed := make(map[string]bool)
		for _, validator := range genesis.Validators {
			if !names[validator.Name] {
				return fail("validators: %q is not an account; its seat would have no key to sign with", validator.Name)
			}
			if listed[validator.Name] {
				return fail("validators: %q is listed twice", validator.Name)
			}
			listed[validator.Name] = true
			if validator.Power < 1 {
				return fail("validators: the power of %q is %d, below 1", validator.Name, validator.Power)
			}
			// consensus.NewValidatorSet would cut a larger power down to the cap without a word, and so
			// change the split of power between the seats.
			if validator.Power > consensus.MaxValidatorPower {
				return fail("validators: the power of %q is %d, above %d (2^40, consensus.MaxValidatorPower)", validator.Name, validator.Power, consensus.MaxValidatorPower)
			}
			validatorRule.StaticValidators = append(validatorRule.StaticValidators, StaticValidator{Name: validator.Name, Power: validator.Power})
		}
	case genesis.ValidatorThreshold != "":
		threshold, err := ParseAmount(genesis.ValidatorThreshold)
		if err != nil {
			return fail("validatorThreshold %q: %v", genesis.ValidatorThreshold, err)
		}
		if threshold.Sign() == 0 {
			return fail("validatorThreshold must be above 0")
		}
		validatorRule.Threshold = threshold
	default:
		return fail("neither validators nor validatorThreshold is set; a network needs one of the two")
	}

	state := LedgerState{Accounts: accounts, Parameters: genesis.Parameters}
	validators := validatorRule.SetFor(state)
	if len(validators.Validators()) == 0 {
		return fail("it seats no name: no account's stake reaches validatorThreshold")
	}
	// The same bar as every later parameter vote, for the genesis validator count.
	if parameters := genesis.Parameters; !parameters.Acceptable(len(validators.Validators())) {
		return fail("parameters a vote could never set: transactionCost must be >= 0; blockIntervalMs >= 0; "+
			"stepTimeoutMs must cover the gossip fan-out (>= %d ms per relay hop for %d validators with maxPeers %d); "+
			"neither timing may exceed %d ms (one day); maxPeers 0..%d; maxMessageBytes 0 or %d..%d; "+
			"newKeyFee, maxPaymentAmount and maxAccountBalance above 0; changeDelayBlocks 1..%d; retainBlocks %d..%d",
			AssumedGossipHopMs, len(validators.Validators()), parameters.MaxPeers, MaxTimingMs, MaxPeersLimit,
			MinMessageBytes, MaxMessageBytesLimit, MaxChangeDelayBlocks, MinRetainBlocks, MaxRetainBlocks)
	}
	for _, account := range accounts {
		if account.Amount.Cmp(genesis.Parameters.MaxAccountBalance) > 0 {
			return fail("accounts: the amount %s of %q is above maxAccountBalance (%s)", FormatAmount(account.Amount), account.Name, FormatAmount(genesis.Parameters.MaxAccountBalance))
		}
		// A bank of record co-signs its customers' key changes, so it must be a bank: a name the
		// genesis seats (R4).
		if account.Bank != "" && !validators.Contains(account.Bank) {
			return fail("accounts: the bank %q of %q is not a name the genesis seats", account.Bank, account.Name)
		}
	}

	genesisHash := GenesisHash(genesis.NetworkName, state, validatorRule)
	chain := Chain{Scheme: signing.MLDSA65{}, NetworkID: NetworkID(genesis.NetworkName, genesisHash), GenesisHash: genesisHash, ValidatorRule: validatorRule}
	return chain, state, nil
}

// GenesisHash is the hash of a network's genesis (spec 2.3): the network's name, the digest of its
// genesis state (the accounts, the parameters, the governance sequence and the height), and the rule
// that seats its validators. Block 1 builds on it, and the effective network id that every signature
// binds is made from it (NetworkID). So a re-genesis that changes anything is another network: the old
// payments and certificates do not verify on it (E20).
//
// The rule is written as "static", then each static validator's name and power in ascending byte order
// of name, or as "dynamic" and the threshold.
func GenesisHash(networkName string, genesisState LedgerState, validatorRule ValidatorRule) [32]byte {
	stateDigest := genesisState.Digest()
	fields := [][]byte{[]byte("go-ledger genesis v2"), []byte(networkName), stateDigest[:]}
	if len(validatorRule.StaticValidators) > 0 {
		fields = append(fields, []byte("static"))
		byName := func(first, second StaticValidator) int { return strings.Compare(first.Name, second.Name) }
		for _, validator := range slices.SortedFunc(slices.Values(validatorRule.StaticValidators), byName) {
			fields = append(fields, []byte(validator.Name), []byte(strconv.FormatInt(validator.Power, 10)))
		}
	} else {
		fields = append(fields, []byte("dynamic"), []byte(FormatAmount(validatorRule.Threshold)))
	}
	return lengthPrefixedDigest(fields...)
}

// NetworkID is the effective network id (spec 2.3): the network's name, a "/", and the genesis hash in
// lowercase hex, for example "village/3f9a…". Nodes sign with it and the wallet pins it.
func NetworkID(networkName string, genesisHash [32]byte) string {
	return networkName + "/" + hex.EncodeToString(genesisHash[:])
}
