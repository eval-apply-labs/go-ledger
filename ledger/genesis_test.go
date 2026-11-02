package ledger

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/signing"
)

// The genesis hash is SHA-256 over lp() fields (spec 2.3), built here by hand: the tag "go-ledger
// genesis v2", the network's name, its gossip port in decimal, the genesis state's digest (32 raw
// bytes; LedgerState.Digest, so the parameters count), then the rule: "static" and each validator's
// name and power in ascending byte order of name, or "dynamic" and the threshold. The effective network
// id is the name, "/" and the hash in hex. A genesis that differs in anything gets another id, so
// nothing signed for one network verifies on the other (E20).
func TestNetworkIdBindsTheGenesis(t *testing.T) {
	genesis := buildLedgerState(0, []Account{namedAccount("bank-a", "1000"), namedAccount("alice", "50")}, rational("0.1"))
	static := ValidatorRule{StaticValidators: []StaticValidator{{Name: "bank-b", Power: 2}, {Name: "bank-a", Power: 1}}}
	dynamic := ValidatorRule{Threshold: rational("100.5")}
	stateDigest := genesis.Digest()

	want := sha256.Sum256(lengthPrefixedPreimage("go-ledger genesis v2", "village", "8090", string(stateDigest[:]), "static", "bank-a", "1", "bank-b", "2"))
	if GenesisHash("village", 8090, genesis, static) != want {
		t.Fatal("the genesis hash does not follow the spec 2.3 layout in static mode")
	}
	if sha256.Sum256(lengthPrefixedPreimage("go-ledger genesis v2", "village", "8090", string(stateDigest[:]), "dynamic", "100.5")) != GenesisHash("village", 8090, genesis, dynamic) {
		t.Fatal("the genesis hash does not follow the spec 2.3 layout in dynamic mode")
	}
	networkID := NetworkID("village", want)
	if networkID != "village/"+hex.EncodeToString(want[:]) {
		t.Fatalf("the network id is %q, want the name, a slash and the genesis hash in hex", networkID)
	}
	reordered := ValidatorRule{StaticValidators: []StaticValidator{static.StaticValidators[1], static.StaticValidators[0]}}
	if NetworkID("village", GenesisHash("village", 8090, genesis, reordered)) != networkID {
		t.Error("the same genesis, its validators listed in another order, gives another network id")
	}

	higherCost := genesis
	higherCost.Parameters.TransactionCost = rational("0.2")
	otherBalance := buildLedgerState(0, []Account{namedAccount("bank-a", "1000"), namedAccount("alice", "51")}, rational("0.1"))
	otherPower := ValidatorRule{StaticValidators: []StaticValidator{{Name: "bank-b", Power: 3}, {Name: "bank-a", Power: 1}}}
	seen := map[string]bool{networkID: true}
	for change, id := range map[string]string{
		"another name":        NetworkID("village-2", GenesisHash("village-2", 8090, genesis, static)),
		"another gossip port": NetworkID("village", GenesisHash("village", 8091, genesis, static)),
		"another parameter":   NetworkID("village", GenesisHash("village", 8090, higherCost, static)),
		"another balance":     NetworkID("village", GenesisHash("village", 8090, otherBalance, static)),
		"another power":       NetworkID("village", GenesisHash("village", 8090, genesis, otherPower)),
		"dynamic mode":        NetworkID("village", GenesisHash("village", 8090, genesis, dynamic)),
		"another threshold":   NetworkID("village", GenesisHash("village", 8090, genesis, ValidatorRule{Threshold: rational("100")})),
	} {
		if seen[id] {
			t.Errorf("a genesis with %s gives a network id another genesis has", change)
		}
		seen[id] = true
	}
}

// exampleGenesis is the genesis of spec 2.5: two banks, seated with power 1 each, the second a customer
// of the first, and alice, a customer of the first. The banks publish their public faces and the IP
// addresses they gossip at, on port 8090. The accounts are listed out of name order, as a file may
// list them.
func exampleGenesis() Genesis {
	return Genesis{
		NetworkName: "village",
		GossipPort:  8090,
		Parameters:  DefaultParameters(),
		Validators:  []GenesisValidator{{Name: "bank-a", Power: 1}, {Name: "bank-b", Power: 1}},
		Accounts: []GenesisAccount{
			{Name: "bank-a", PublicKey: testSigner("bank-a").PublicKeyBase64(), Amount: "1000", Delegate: "bank-a",
				PublicAddress: "https://bank-a.example", IPAddresses: []string{"10.0.0.2"}},
			{Name: "bank-b", PublicKey: testSigner("bank-b").PublicKeyBase64(), Amount: "1000", Bank: "bank-a", Delegate: "bank-b",
				PublicAddress: "https://bank-b.example", IPAddresses: []string{"10.0.0.3", "2001:db8::3"}},
			{Name: "alice", PublicKey: testSigner("alice").PublicKeyBase64(), Amount: "50", Bank: "bank-a", Description: "the baker"},
		},
	}
}

// expectGenesisRefused asserts that ValidateGenesis refuses genesis with a message that names what to
// fix.
func expectGenesisRefused(t *testing.T, genesis Genesis, wantSubstring string) {
	t.Helper()
	_, _, err := ValidateGenesis(genesis)
	if err == nil {
		t.Fatalf("ValidateGenesis accepted a genesis it must refuse (want an error mentioning %q)", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("error %q does not mention %q", err, wantSubstring)
	}
}

// otherSpellingOf spells key differently: the last character before the padding carries two bits per
// "=" the decoder ignores, and changing them gives another string for the same key.
func otherSpellingOf(t *testing.T, key string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := len(strings.TrimRight(key, "=")) - 1
	if last == len(key)-1 {
		return key[:20] + "\n" + key[20:] // no padding, so no unused bits: a newline, which the decoder skips
	}
	spelling := key[:last] + string(alphabet[strings.IndexByte(alphabet, key[last])^1]) + key[last+1:]
	if raw, err := base64.StdEncoding.DecodeString(spelling); err != nil || base64.StdEncoding.EncodeToString(raw) != key {
		t.Fatalf("%q does not decode to the key %q", spelling, key)
	}
	return spelling
}

// A name is the identity, so it has one spelling (spec 3.2) and belongs to one account: "Alice" and
// "alice" would be two accounts for one person. The network's name is a name too: it begins the network
// id that every signature binds.
func TestGenesisRefusesNonCanonicalOrDuplicateNames(t *testing.T) {
	if _, _, err := ValidateGenesis(exampleGenesis()); err != nil {
		t.Fatalf("the example genesis was refused: %v", err)
	}
	for _, testCase := range []struct {
		description string
		change      func(genesis *Genesis)
		want        string
	}{
		{"a network name in capitals", func(genesis *Genesis) { genesis.NetworkName = "Village" }, `networkName "Village" is not a name`},
		{"no network name", func(genesis *Genesis) { genesis.NetworkName = "" }, `networkName "" is not a name`},
		{"a network name with a slash", func(genesis *Genesis) { genesis.NetworkName = "village/2" }, `networkName "village/2" is not a name`},
		{"a name in capitals", func(genesis *Genesis) { genesis.Accounts[2].Name = "Alice" }, `name "Alice" is not a name`},
		{"no name", func(genesis *Genesis) { genesis.Accounts[2].Name = "" }, `name "" is not a name`},
		{"a name of 33 letters", func(genesis *Genesis) { genesis.Accounts[2].Name = strings.Repeat("a", 33) }, "is not a name"},
		{"a name ending in a hyphen", func(genesis *Genesis) { genesis.Accounts[2].Name = "alice-" }, `name "alice-" is not a name`},
		{"the dash that clears a field", func(genesis *Genesis) { genesis.Accounts[2].Name = "-" }, `name "-" is not a name`},
		{"a name given twice", func(genesis *Genesis) {
			genesis.Accounts = append(genesis.Accounts, GenesisAccount{Name: "alice", PublicKey: testSigner("carol").PublicKeyBase64(), Amount: "1"})
		}, `the name "alice" is given twice`},
	} {
		t.Run(testCase.description, func(t *testing.T) {
			genesis := exampleGenesis()
			testCase.change(&genesis)
			expectGenesisRefused(t, genesis, testCase.want)
		})
	}
}

// One key belongs to one name: a key given to two names would let its holder sign as both, its votes
// counting twice. A key has one spelling, the canonical one, as keys are compared as strings, so
// another spelling of a key the genesis already holds is refused too.
func TestGenesisRefusesDuplicateKeys(t *testing.T) {
	bankAKey := testSigner("bank-a").PublicKeyBase64()
	genesis := exampleGenesis()
	genesis.Accounts[2].PublicKey = bankAKey
	expectGenesisRefused(t, genesis, `the publicKey of "alice" is another account's too`)

	genesis = exampleGenesis()
	genesis.Accounts[2].PublicKey = otherSpellingOf(t, bankAKey)
	expectGenesisRefused(t, genesis, `the publicKey of "alice" is not a key of the network's scheme`)
}

// A bank of record co-signs its customers' key changes, so it must be a bank, one the genesis seats
// (R4): otherwise any name could be made a customer's bank and co-sign for a thief. A name that is not
// an account is not seated either. In dynamic mode a name is seated by the stake it holds.
func TestGenesisRefusesABankThatIsNotSeated(t *testing.T) {
	genesis := exampleGenesis()
	genesis.Accounts[2].Bank = "nobody"
	expectGenesisRefused(t, genesis, `the bank "nobody" of "alice" is not a name the genesis seats`)

	genesis = exampleGenesis()
	genesis.Accounts = append(genesis.Accounts, GenesisAccount{Name: "carol", PublicKey: testSigner("carol").PublicKeyBase64(), Amount: "5", Bank: "alice"})
	expectGenesisRefused(t, genesis, `the bank "alice" of "carol" is not a name the genesis seats`)

	dynamic := exampleGenesis()
	dynamic.Validators, dynamic.ValidatorThreshold = nil, "500" // the banks hold 1000 each, alice 50
	if _, _, err := ValidateGenesis(dynamic); err != nil {
		t.Fatalf("a dynamic genesis whose banks are seated by their stake was refused: %v", err)
	}
	dynamic.Accounts[2].Delegate = "alice"
	dynamic.Accounts = append(dynamic.Accounts, GenesisAccount{Name: "carol", PublicKey: testSigner("carol").PublicKeyBase64(), Amount: "5", Bank: "alice"})
	expectGenesisRefused(t, dynamic, `the bank "alice" of "carol" is not a name the genesis seats`)
}

// A name is never its own bank (R2): the bank co-signs its customer's key change, and a name that is its
// own bank would need no second signature. A bank has no bank of record, or another bank.
func TestGenesisRefusesAnOwnBank(t *testing.T) {
	genesis := exampleGenesis()
	genesis.Accounts[0].Bank = "bank-a" // seated, but its own name
	expectGenesisRefused(t, genesis, `the bank of "bank-a" is its own name`)

	genesis = exampleGenesis()
	genesis.Accounts[2].Bank = "alice"
	expectGenesisRefused(t, genesis, `the bank of "alice" is its own name`)
}

// The effective network id that ValidateGenesis gives is the network's name, a slash, and the 64 hex
// characters of the genesis hash (spec 2.3), over the gossip port, the genesis state (the accounts in
// name order, as the file gives them and with no freeze or pending change, the parameters, governance
// sequence 0 and height 0) and the validator rule. The chain carries the gossip port, which nodes read.
// The order the file lists them in does not matter; anything else it says does.
func TestNetworkIdIsTheNamePlusTheGenesisHash(t *testing.T) {
	chain, state, err := ValidateGenesis(exampleGenesis())
	if err != nil {
		t.Fatal(err)
	}
	amount := func(text string) *big.Rat {
		parsed, err := ParseAmount(text)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	wantState := LedgerState{Parameters: DefaultParameters(), Accounts: []Account{
		{Name: "alice", PublicKey: testSigner("alice").PublicKeyBase64(), Amount: amount("50"), Bank: "bank-a", Description: "the baker"},
		{Name: "bank-a", PublicKey: testSigner("bank-a").PublicKeyBase64(), Amount: amount("1000"), Delegate: "bank-a",
			PublicAddress: "https://bank-a.example", IPAddresses: []string{"10.0.0.2"}},
		{Name: "bank-b", PublicKey: testSigner("bank-b").PublicKeyBase64(), Amount: amount("1000"), Bank: "bank-a", Delegate: "bank-b",
			PublicAddress: "https://bank-b.example", IPAddresses: []string{"10.0.0.3", "2001:db8::3"}},
	}}
	wantRule := ValidatorRule{StaticValidators: []StaticValidator{{Name: "bank-a", Power: 1}, {Name: "bank-b", Power: 1}}}
	if state.Digest() != wantState.Digest() || !state.NamesAscending() {
		t.Fatalf("the genesis state is %+v, want %+v", state, wantState)
	}
	if !slices.Equal(chain.ValidatorRule.StaticValidators, wantRule.StaticValidators) || chain.ValidatorRule.Threshold != nil {
		t.Fatalf("the validator rule is %+v, want %+v", chain.ValidatorRule, wantRule)
	}
	wantHash := GenesisHash("village", 8090, wantState, wantRule)
	if chain.GenesisHash != wantHash || chain.NetworkID != "village/"+hex.EncodeToString(wantHash[:]) || len(chain.NetworkID) != len("village/")+64 {
		t.Fatalf("the network id is %q, want the name, a slash and the genesis hash in hex", chain.NetworkID)
	}
	if chain.GossipPort != 8090 {
		t.Errorf("the chain's gossip port is %d, want the genesis's, 8090", chain.GossipPort)
	}
	if chain.Scheme != signing.NetworkScheme() {
		t.Errorf("the chain's scheme is %T, want the network's, %T", chain.Scheme, signing.NetworkScheme())
	}

	reordered := exampleGenesis()
	slices.Reverse(reordered.Accounts)
	slices.Reverse(reordered.Validators)
	if reorderedChain, _, err := ValidateGenesis(reordered); err != nil || reorderedChain.NetworkID != chain.NetworkID {
		t.Errorf("the same genesis, listed in another order, gives the network id %q (%v), want %q", reorderedChain.NetworkID, err, chain.NetworkID)
	}
	for change, otherGenesis := range map[string]func(genesis *Genesis){
		"another public address": func(genesis *Genesis) { genesis.Accounts[1].PublicAddress = "https://bank-b.example.org" },
		"another IP address":     func(genesis *Genesis) { genesis.Accounts[1].IPAddresses[1] = "2001:db8::4" },
		"IP addresses reordered": func(genesis *Genesis) { slices.Reverse(genesis.Accounts[1].IPAddresses) },
		"another gossip port":    func(genesis *Genesis) { genesis.GossipPort = 8091 },
	} {
		changed := exampleGenesis()
		otherGenesis(&changed)
		if otherChain, _, err := ValidateGenesis(changed); err != nil || otherChain.NetworkID == chain.NetworkID {
			t.Errorf("a genesis with %s gives the same network id (%v)", change, err)
		}
	}
}

// The genesis must be a state the ledger itself could reach, under a rule and parameters a network can
// run with. Each refusal names the field to fix; a key the scheme refuses, the scheme's reason too.
func TestGenesisRefusesWhatNoLedgerCouldHold(t *testing.T) {
	aliceKey, _ := base64.StdEncoding.DecodeString(exampleGenesis().Accounts[2].PublicKey)
	scheme := signing.NetworkScheme()
	shortKey, shortKeyReason := aliceKey[:len(aliceKey)-1], scheme.ValidatePublicKey(aliceKey[:len(aliceKey)-1])
	if shortKeyReason == nil {
		t.Fatal("premise: the scheme refuses a key a byte short")
	}
	for _, testCase := range []struct {
		description string
		change      func(genesis *Genesis)
		want        string
	}{
		{"a key that is not base64", func(genesis *Genesis) { genesis.Accounts[2].PublicKey = "X" }, `the publicKey of "alice" is not a key of the network's scheme`},
		{"a key a byte short", func(genesis *Genesis) { genesis.Accounts[2].PublicKey = base64.StdEncoding.EncodeToString(shortKey) },
			`the publicKey of "alice" is not a key of the network's scheme, ` + scheme.Name() + ": " + shortKeyReason.Error()},
		{"a key in another spelling", func(genesis *Genesis) {
			genesis.Accounts[2].PublicKey = otherSpellingOf(t, genesis.Accounts[2].PublicKey)
		}, `the publicKey of "alice" is not a key of the network's scheme`},
		{"a negative amount", func(genesis *Genesis) { genesis.Accounts[2].Amount = "-5" }, `the amount "-5" of "alice"`},
		{"an amount in exponent form", func(genesis *Genesis) { genesis.Accounts[2].Amount = "1e3" }, `the amount "1e3" of "alice"`},
		{"an amount below the 1e-8 unit", func(genesis *Genesis) { genesis.Accounts[2].Amount = "0.000000001" }, `the amount "0.000000001" of "alice"`},
		{"no amount", func(genesis *Genesis) { genesis.Accounts[2].Amount = "" }, `the amount "" of "alice"`},
		{"an amount above the balance cap", func(genesis *Genesis) { genesis.Accounts[2].Amount = "10000000.00000001" }, `the amount 10000000.00000001 of "alice" is above maxAccountBalance (10000000)`},
		{"a delegate that is no account", func(genesis *Genesis) { genesis.Accounts[2].Delegate = "nobody" }, `the delegate "nobody" of "alice" is not an account`},
		{"no gossip port", func(genesis *Genesis) { genesis.GossipPort = 0 }, "gossipPort 0 is not a port from 1 to 65535"},
		{"a negative gossip port", func(genesis *Genesis) { genesis.GossipPort = -1 }, "gossipPort -1 is not a port from 1 to 65535"},
		{"a gossip port above 65535", func(genesis *Genesis) { genesis.GossipPort = 65536 }, "gossipPort 65536 is not a port from 1 to 65535"},
		{"a validator that is no account", func(genesis *Genesis) {
			genesis.Validators = append(genesis.Validators, GenesisValidator{Name: "ghost", Power: 1})
		}, `validators: "ghost" is not an account`},
		{"a validator listed twice", func(genesis *Genesis) {
			genesis.Validators = append(genesis.Validators, GenesisValidator{Name: "bank-a", Power: 2})
		}, `validators: "bank-a" is listed twice`},
		{"a validator of power 0", func(genesis *Genesis) { genesis.Validators[1].Power = 0 }, `the power of "bank-b" is 0, below 1`},
		{"a validator of negative power", func(genesis *Genesis) { genesis.Validators[1].Power = -1 }, `the power of "bank-b" is -1, below 1`},
		{"a validator above the power cap", func(genesis *Genesis) { genesis.Validators[1].Power = consensus.MaxValidatorPower + 1 },
			`the power of "bank-b" is 1099511627777, above 1099511627776 (2^40, consensus.MaxValidatorPower)`},
		{"both validators and a threshold", func(genesis *Genesis) { genesis.ValidatorThreshold = "100" }, "both validators and validatorThreshold"},
		{"neither validators nor a threshold", func(genesis *Genesis) { genesis.Validators = nil }, "neither validators nor validatorThreshold"},
		{"a threshold that is a fraction", func(genesis *Genesis) { genesis.Validators, genesis.ValidatorThreshold = nil, "1/3" }, `validatorThreshold "1/3"`},
		{"a threshold of 0", func(genesis *Genesis) { genesis.Validators, genesis.ValidatorThreshold = nil, "0" }, "validatorThreshold must be above 0"},
		{"a threshold no stake reaches", func(genesis *Genesis) {
			genesis.Validators, genesis.ValidatorThreshold = nil, "1000.00000001" // the banks hold 1000 each
		}, "it seats no name"},
		{"a negative block interval", func(genesis *Genesis) { genesis.Parameters.BlockIntervalMs = -5 }, "parameters a vote could never set"},
		{"a step timeout above one day", func(genesis *Genesis) { genesis.Parameters.StepTimeoutMs = 100_000_000 }, "exceed 86400000 ms"},
		{"a step timeout too short for the names it seats", func(genesis *Genesis) {
			// The fan-out counts every seated name, in dynamic mode too: the stake seats bank-a and
			// bank-b, and with maxPeers 1 two names need two relay hops (500 ms), one name only one.
			genesis.Validators, genesis.ValidatorThreshold = nil, "500"
			genesis.Parameters.MaxPeers, genesis.Parameters.StepTimeoutMs = 1, 400
		}, "stepTimeoutMs must cover the gossip fan-out (>= 250 ms per relay hop for 2 validators with maxPeers 1)"},
		{"a message bound too small for a payment", func(genesis *Genesis) { genesis.Parameters.MaxMessageBytes = 100 }, fmt.Sprintf("maxMessageBytes 0 or %d..1048576", MinMessageBytes())},
		{"no transaction cost", func(genesis *Genesis) { genesis.Parameters.TransactionCost = nil }, "parameters a vote could never set"},
		{"a change delay of 0", func(genesis *Genesis) { genesis.Parameters.ChangeDelayBlocks = 0 }, "changeDelayBlocks 1..2592000"},
		{"a retention below its floor", func(genesis *Genesis) { genesis.Parameters.RetainBlocks = 999 }, "retainBlocks 1000..315360000"},
	} {
		t.Run(testCase.description, func(t *testing.T) {
			genesis := exampleGenesis()
			testCase.change(&genesis)
			expectGenesisRefused(t, genesis, testCase.want)
		})
	}

	// A balance may be zero, as an account's is once it pays out, so a genesis written from captured
	// state keeps its names; and it may be the cap itself.
	genesis := exampleGenesis()
	genesis.Accounts[2].Amount = "0"
	genesis.Accounts[1].Amount = "10000000"
	if _, state, err := ValidateGenesis(genesis); err != nil {
		t.Fatalf("a balance of zero or at the cap was refused: %v", err)
	} else if alice, _ := FindAccount(state.Accounts, "alice"); alice.Amount.Sign() != 0 {
		t.Fatalf("alice holds %s, want 0", FormatAmount(alice.Amount))
	}
	// A seat's power may be the power cap itself.
	genesis = exampleGenesis()
	genesis.Validators[1].Power = consensus.MaxValidatorPower
	if _, _, err := ValidateGenesis(genesis); err != nil {
		t.Fatalf("a validator power at the cap was refused: %v", err)
	}
}

// The genesis file of spec 2.5, with real keys. ParseGenesis reads every field of it. It refuses a
// file that leaves a parameter out, since a missing message bound would read as 0, no bound at all,
// and any key it does not know, so a misspelt field is never read as one left empty. A malformed amount
// parameter is refused with its name.
func TestGenesisFileGivesEveryParameterAndNoUnknownKey(t *testing.T) {
	exampleFile := `{
  "networkName": "village",
  "gossipPort": 8090,
  "parameters": {
    "transactionCost": "0.1", "blockIntervalMs": 1000, "stepTimeoutMs": 3000, "maxPeers": 0,
    "maxMessageBytes": MAX-MESSAGE-BYTES, "newKeyFee": "1", "changeDelayBlocks": 86400,
    "retainBlocks": 604800, "maxPaymentAmount": "100000", "maxAccountBalance": "10000000"
  },
  "validators": [ { "name": "bank-a", "power": 1 }, { "name": "bank-b", "power": 1 } ],
  "validatorThreshold": "",
  "accounts": [
    { "name": "bank-a", "publicKey": "KEY-A", "amount": "1000", "bank": "", "delegate": "bank-a",
      "description": "", "publicAddress": "https://bank-a.example", "ipAddresses": [ "10.0.0.2" ] },
    { "name": "bank-b", "publicKey": "KEY-B", "amount": "1000", "bank": "bank-a", "delegate": "bank-b",
      "description": "", "publicAddress": "https://bank-b.example", "ipAddresses": [ "10.0.0.3", "2001:db8::3" ] },
    { "name": "alice", "publicKey": "KEY-ALICE", "amount": "50", "bank": "bank-a", "delegate": "",
      "description": "the baker", "publicAddress": "", "ipAddresses": [] }
  ]
}`
	exampleFile = strings.NewReplacer("MAX-MESSAGE-BYTES", fmt.Sprint(DefaultParameters().MaxMessageBytes), "KEY-ALICE", testSigner("alice").PublicKeyBase64(),
		"KEY-A", testSigner("bank-a").PublicKeyBase64(), "KEY-B", testSigner("bank-b").PublicKeyBase64()).Replace(exampleFile)
	genesis, err := ParseGenesis([]byte(exampleFile))
	if err != nil {
		t.Fatal(err)
	}
	want := exampleGenesis()
	want.Accounts[2].IPAddresses = []string{} // the file's [], which leaves alice none, as nothing would
	if genesis.Parameters.Digest() != want.Parameters.Digest() || !slices.Equal(genesis.Validators, want.Validators) ||
		!reflect.DeepEqual(genesis.Accounts, want.Accounts) || genesis.NetworkName != "village" || genesis.GossipPort != 8090 || genesis.ValidatorThreshold != "" {
		t.Fatalf("read %+v, want %+v", genesis, want)
	}

	// withParameters gives the example file with its parameters changed by change.
	withParameters := func(change func(parameters map[string]any)) []byte {
		var file map[string]any
		if err := json.Unmarshal([]byte(exampleFile), &file); err != nil {
			t.Fatal(err)
		}
		change(file["parameters"].(map[string]any))
		changed, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	for _, name := range genesisParameterNames {
		leftOut := withParameters(func(parameters map[string]any) { delete(parameters, name) })
		if _, err := ParseGenesis(leftOut); err == nil || !strings.Contains(err.Error(), name+" is missing") {
			t.Errorf("a genesis without %s: error %v, want one that names it", name, err)
		}
	}
	// A malformed amount parameter is refused with its name, so the operator knows which one to fix.
	for _, name := range []string{"transactionCost", "newKeyFee", "maxPaymentAmount", "maxAccountBalance"} {
		for _, testCase := range []struct {
			value      any
			wantReason string
		}{
			{"-1", "plain non-negative decimal"},
			{"1e5", "plain non-negative decimal"},
			{"1/2", "plain non-negative decimal"},
			{"0.000000001", "plain non-negative decimal"},
			{nil, "plain non-negative decimal"},
			{0.5, "cannot unmarshal"},
		} {
			malformed := withParameters(func(parameters map[string]any) { parameters[name] = testCase.value })
			want := "parameters: " + name + ": "
			if _, err := ParseGenesis(malformed); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), testCase.wantReason) {
				t.Errorf("a genesis with %s %#v: error %v, want one starting %q and mentioning %q", name, testCase.value, err, want, testCase.wantReason)
			}
		}
	}
	for _, testCase := range []struct{ description, old, new, want string }{
		{"a parameter in another case", `"maxMessageBytes"`, `"MaxMessageBytes"`, "maxMessageBytes is missing"},
		{"an unknown parameter", `"maxPeers": 0,`, `"maxPeers": 0, "minBlockIntervalMs": 1000,`, `"minBlockIntervalMs" is not a parameter`},
		{"an unknown field", `"validatorThreshold": "",`, `"validatorThreshold": "", "networkId": "village",`, `unknown field "networkId"`},
		{"an unknown account field", `"bank": "bank-a",`, `"bank": "bank-a", "frozen": true,`, `unknown field "frozen"`},
		{"an unknown validator field", `"power": 1 }`, `"power": 1, "url": "http://10.0.0.2:8080" }`, `unknown field "url"`},
		{"a parameter amount that is negative", `"transactionCost": "0.1"`, `"transactionCost": "-1"`, "parameters: transactionCost: amount must be a plain non-negative decimal"},
	} {
		changed := strings.Replace(exampleFile, testCase.old, testCase.new, 1)
		if changed == exampleFile {
			t.Fatalf("%s: the test did not change the file", testCase.description)
		}
		if _, err := ParseGenesis([]byte(changed)); err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: error %v, want one mentioning %q", testCase.description, err, testCase.want)
		}
	}
}
