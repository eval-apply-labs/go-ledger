package ledger

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

// An amount is written as 1 to maxIntegerDigits digits, optionally followed by a point and 1 to
// maxFractionDigits more: at most 27 characters. Every amount the ledger holds or carries is below
// 10^18 (the caps keep balances there), so it always fits.
const (
	maxIntegerDigits  = 18
	maxFractionDigits = 8
)

// amountScale is 10^8: every amount is a whole number of 1e-8 units.
var amountScale = big.NewInt(100_000_000)

var (
	errAmountForm    = errors.New("amount must be a plain non-negative decimal: 1 to 18 digits, optionally a point and 1 to 8 more, such as 12 or 0.5")
	errAmountMissing = errors.New("amount is missing: only a ⅔ name action's amounts and a freeze's fee may be null")
)

// ParseAmount is the one parser for a money amount given as text: in JSON (payments, accounts,
// checkpoints, snapshots, parameters, reassignments, name requests) and in the genesis file. It accepts
// only the plain decimal form above: no sign, no exponent, no fraction a/b, no leading or trailing
// point. Leading zeros and trailing fraction zeros are accepted. The text is checked before any number
// is built, so a short input can never turn into a huge number, and the value is always a whole number
// of 1e-8 units.
func ParseAmount(text string) (*big.Rat, error) {
	integerDigits, fractionDigits, hasPoint := strings.Cut(text, ".")
	if !digitsOnly(integerDigits, maxIntegerDigits) || (hasPoint && !digitsOnly(fractionDigits, maxFractionDigits)) {
		return nil, errAmountForm
	}
	units, _ := new(big.Int).SetString(integerDigits+fractionDigits+strings.Repeat("0", maxFractionDigits-len(fractionDigits)), 10)
	return new(big.Rat).SetFrac(units, amountScale), nil
}

// digitsOnly reports whether text is 1 to maxDigits ASCII digits.
func digitsOnly(text string, maxDigits int) bool {
	if len(text) == 0 || len(text) > maxDigits {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// FormatAmount writes an amount in its one canonical form, which JSON, signed fields and digests all
// use: the integer part without leading zeros ("0" when it is zero), then, only for a non-zero
// fraction, a point and the fraction without trailing zeros. Examples: "0.5", "12", "1000.00000001".
// Amounts are whole numbers of 1e-8 units (ParseAmount makes them so, and sums and differences keep
// them so), so eight decimals write them exactly. A nil amount, never in honest state, is "".
func FormatAmount(amount *big.Rat) string {
	if amount == nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimRight(amount.FloatString(maxFractionDigits), "0"), ".")
}

// amountJSON reads and writes one amount field. It reads the text through ParseAmount, so every amount
// read from a peer, a client or a file is checked before a number is built, and JSON null as nil. It
// writes the canonical form, or null for nil. The types below that carry amounts decode and encode
// their other fields as usual (through a local type without the methods) and their amounts through it.
// Their MarshalJSON methods have value receivers: Go calls a pointer-receiver method only on an
// addressable value, so a plain value, or a field of one, would otherwise be written by big.Rat as a/b.
type amountJSON struct{ amount **big.Rat } // implements json.Marshaler and json.Unmarshaler

// The compiler checks that these types implement json.Marshaler and json.Unmarshaler. encoding/json
// calls a type's MarshalJSON and UnmarshalJSON only if their signatures are exactly right. After a
// slip it would silently use its default encoding, and amounts would no longer pass through
// FormatAmount and ParseAmount. UnmarshalJSON writes into the value, so it takes a pointer receiver,
// and it is the pointer that implements json.Unmarshaler.
var (
	_ json.Marshaler   = amountJSON{}
	_ json.Unmarshaler = (*amountJSON)(nil)
	_ json.Marshaler   = Account{}
	_ json.Unmarshaler = (*Account)(nil)
	_ json.Marshaler   = Payment{}
	_ json.Unmarshaler = (*Payment)(nil)
	_ json.Marshaler   = Reassignment{}
	_ json.Unmarshaler = (*Reassignment)(nil)
	_ json.Marshaler   = LedgerParameters{}
	_ json.Unmarshaler = (*LedgerParameters)(nil)
	_ json.Marshaler   = NameRequest{}
	_ json.Unmarshaler = (*NameRequest)(nil)
)

func (amountField amountJSON) MarshalJSON() ([]byte, error) {
	if *amountField.amount == nil {
		return []byte("null"), nil
	}
	return json.Marshal(FormatAmount(*amountField.amount))
}

func (amountField *amountJSON) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*amountField.amount = nil
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	amount, err := ParseAmount(text)
	if err != nil {
		return err
	}
	*amountField.amount = amount
	return nil
}

// MarshalJSON writes an Account with its amount in canonical form.
func (account Account) MarshalJSON() ([]byte, error) {
	type plain Account
	return json.Marshal(struct {
		plain
		Amount amountJSON
	}{plain(account), amountJSON{&account.Amount}})
}

// UnmarshalJSON decodes an Account, reading Amount through ParseAmount. The amount is required.
func (account *Account) UnmarshalJSON(data []byte) error {
	type plain Account
	if err := json.Unmarshal(data, &struct {
		*plain
		Amount amountJSON
	}{(*plain)(account), amountJSON{&account.Amount}}); err != nil {
		return err
	}
	return requireAmounts(account.Amount)
}

// MarshalJSON writes a Payment with its amount in canonical form.
func (payment Payment) MarshalJSON() ([]byte, error) {
	type plain Payment
	return json.Marshal(struct {
		plain
		Amount amountJSON
	}{plain(payment), amountJSON{&payment.Amount}})
}

// UnmarshalJSON decodes a Payment, reading Amount through ParseAmount. The amount is required.
func (payment *Payment) UnmarshalJSON(data []byte) error {
	type plain Payment
	if err := json.Unmarshal(data, &struct {
		*plain
		Amount amountJSON
	}{(*plain)(payment), amountJSON{&payment.Amount}}); err != nil {
		return err
	}
	return requireAmounts(payment.Amount)
}

// MarshalJSON writes a Reassignment with both amounts in canonical form, or null.
func (reassignment Reassignment) MarshalJSON() ([]byte, error) {
	type plain Reassignment
	return json.Marshal(struct {
		plain
		PreviousAmount, NewAmount amountJSON
	}{plain(reassignment), amountJSON{&reassignment.PreviousAmount}, amountJSON{&reassignment.NewAmount}})
}

// UnmarshalJSON decodes a Reassignment, reading both amounts through ParseAmount. Either may be null.
func (reassignment *Reassignment) UnmarshalJSON(data []byte) error {
	type plain Reassignment
	return json.Unmarshal(data, &struct {
		*plain
		PreviousAmount, NewAmount amountJSON
	}{(*plain)(reassignment), amountJSON{&reassignment.PreviousAmount}, amountJSON{&reassignment.NewAmount}})
}

// MarshalJSON writes LedgerParameters with its four amounts in canonical form.
func (ledgerParameters LedgerParameters) MarshalJSON() ([]byte, error) {
	type plain LedgerParameters
	return json.Marshal(struct {
		plain
		TransactionCost, NewKeyFee, MaxPaymentAmount, MaxAccountBalance amountJSON
	}{plain(ledgerParameters), amountJSON{&ledgerParameters.TransactionCost}, amountJSON{&ledgerParameters.NewKeyFee},
		amountJSON{&ledgerParameters.MaxPaymentAmount}, amountJSON{&ledgerParameters.MaxAccountBalance}})
}

// UnmarshalJSON decodes LedgerParameters (also inside a ParameterChange), reading its four amounts
// through ParseAmount. All four are required.
func (ledgerParameters *LedgerParameters) UnmarshalJSON(data []byte) error {
	type plain LedgerParameters
	if err := json.Unmarshal(data, &struct {
		*plain
		TransactionCost, NewKeyFee, MaxPaymentAmount, MaxAccountBalance amountJSON
	}{(*plain)(ledgerParameters), amountJSON{&ledgerParameters.TransactionCost}, amountJSON{&ledgerParameters.NewKeyFee},
		amountJSON{&ledgerParameters.MaxPaymentAmount}, amountJSON{&ledgerParameters.MaxAccountBalance}}); err != nil {
		return err
	}
	return requireAmounts(ledgerParameters.TransactionCost, ledgerParameters.NewKeyFee, ledgerParameters.MaxPaymentAmount, ledgerParameters.MaxAccountBalance)
}

// requireAmounts refuses a decoded value whose required amount is missing, written as null or left
// out: only a ⅔ name action's amounts and a freeze's fee may be absent.
func requireAmounts(amounts ...*big.Rat) error {
	for _, amount := range amounts {
		if amount == nil {
			return errAmountMissing
		}
	}
	return nil
}
