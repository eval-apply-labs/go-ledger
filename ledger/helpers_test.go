package ledger

import (
	"math/big"
	"slices"
	"strings"

	"github.com/eval-apply-labs/go-ledger/signing"
)

// testNetwork is the network id the ledger tests sign for.
const testNetwork = "testnet"

// testSigners holds the signer of each test account, made on first use: generating a key costs about
// a millisecond, so the tests share a few. The ledger tests do not run in parallel.
var testSigners = map[string]signing.Signer{}

// testSigner returns the signer that holds the key of the test account `name`, the same one each time.
func testSigner(name string) signing.Signer {
	if signer, found := testSigners[name]; found {
		return signer
	}
	signer, err := signing.Generate(signing.NetworkScheme(), testNetwork)
	if err != nil {
		panic(err)
	}
	testSigners[name] = signer
	return signer
}

// namedAccount is the test account `name`, with the key testSigner(name) holds and the given balance.
func namedAccount(name, amount string) Account {
	return Account{Name: name, PublicKey: testSigner(name).PublicKeyBase64(), Amount: rational(amount)}
}

// seatedAccount is the test account "seat", which backs itself with its balance, so the dynamic test
// chain (testChain with no names) seats it. A block whose state seats no name is invalid (rule 24, R9),
// so a fixture on that chain carries it, or a delegation of its own.
func seatedAccount() Account {
	account := namedAccount("seat", "1")
	account.Delegate = "seat"
	return account
}

// testChain is the ledger tests' chain: the network's scheme on testNetwork, with the given names as its static
// validators, each of power 1, or, when none is given, dynamic with no threshold (any stake seats the
// name it backs).
func testChain(validatorNames ...string) Chain {
	var validatorRule ValidatorRule
	for _, name := range validatorNames {
		validatorRule.StaticValidators = append(validatorRule.StaticValidators, StaticValidator{Name: name, Power: 1})
	}
	return Chain{Scheme: signing.NetworkScheme(), NetworkID: testNetwork, ValidatorRule: validatorRule}
}

// signedPayment is a payment of amount from the test account `from` to the name `to`, for the block
// blockID, signed under from's key.
func signedPayment(from, to, amount string, blockID int64) Payment {
	payment, err := SignPayment(testSigner(from), Payment{From: from, To: to, Amount: rational(amount), BlockID: blockID})
	if err != nil {
		panic(err)
	}
	return payment
}

// buildLedgerState builds the state at height with the given accounts, put in name order as the ledger
// keeps them, at the default parameters with the given transaction cost: the state a block at
// height + 1 builds on, or the one a block at height results in.
func buildLedgerState(height int64, accounts []Account, transactionCost *big.Rat) LedgerState {
	sorted := slices.SortedFunc(slices.Values(accounts), func(first, second Account) int { return strings.Compare(first.Name, second.Name) })
	return LedgerState{Accounts: sorted, Parameters: parametersCosting(transactionCost), Height: height}
}

// parametersCosting is the default parameters with the given transaction cost.
func parametersCosting(transactionCost *big.Rat) LedgerParameters {
	parameters := DefaultParameters()
	parameters.TransactionCost = transactionCost
	return parameters
}

// zeroHash is the previous block hash and the nonce the ledger tests build their blocks with.
var zeroHash [32]byte

// signingSetHash is the ValidatorsHash of the set chain seats on state: the hash a block built on state
// carries.
func signingSetHash(chain Chain, state LedgerState) [32]byte {
	return ValidatorsHash(chain.ValidatorRule.SetFor(state).Validators())
}
