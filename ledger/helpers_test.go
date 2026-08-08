package ledger

import "math/big"

// st builds a LedgerState with the given accounts and transaction cost — a convenience for tests
// now that ProposeBlock/ApplyBlock operate on committed state (accounts + parameters).
func st(accounts []Account, transactionCost *big.Rat) LedgerState {
	return LedgerState{Accounts: accounts, Parameters: LedgerParameters{TransactionCost: transactionCost}}
}
