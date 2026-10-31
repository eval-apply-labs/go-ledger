package ledger

import (
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/eval-apply-labs/go-ledger/signing"
)

func TestParseAmountAcceptsPlainDecimals(t *testing.T) {
	for text, want := range map[string]string{
		"0": "0", "12": "12", "0.5": "1/2", "007.50": "15/2", "0.00000001": "1/100000000", "12.50000000": "25/2",
		strings.Repeat("9", 18) + ".99999999": strings.Repeat("9", 26) + "/100000000", // the largest: 10^18 less one unit
	} {
		parsed, err := ParseAmount(text)
		if err != nil {
			t.Errorf("ParseAmount(%q): %v", text, err)
			continue
		}
		if wantRational, _ := new(big.Rat).SetString(want); parsed.Cmp(wantRational) != 0 {
			t.Errorf("ParseAmount(%q) = %s, want %s", text, parsed.RatString(), want)
		}
	}
}

// big.Rat reads fractions a/b, exponents ("1p10000000" is 11 bytes and a 10-million-bit number), other
// bases, and any precision. The parser takes only the plain decimal form, 18 digits and 8 decimals at
// most, so none of these reach big.Rat, and every amount is a whole number of 1e-8 units.
func TestAmountParserRefusesFractionsExponentsAndOverlongDigits(t *testing.T) {
	for _, text := range []string{
		"1/2", "1/10", "5/1", "99/10", "1/3", "010/3", // fractions, the a/b form big.Rat writes
		"1e1000000", "1E5", "1p10000000", "1P4", "0x1p4", "0x10", "0b101", "0o17", "Inf", "NaN", // exponents, bases
		"", "-1", "+1", " 1", "1 ", ".5", "5.", ".", "1.2.3", "1_000", "1,5", "½", "١", // anything else
		strings.Repeat("1", 19), strings.Repeat("1", 19) + ".5", "0.000000001", "1." + strings.Repeat("0", 9), // too many digits
		strings.Repeat("9", 18) + "." + strings.Repeat("9", 9), // 28 characters
	} {
		if _, err := ParseAmount(text); err == nil {
			t.Errorf("ParseAmount(%q) accepted it", text)
		}
	}
}

// One amount has one spelling: the canonical form drops leading zeros, trailing fraction zeros and a
// bare point, and every amount below 10^18 units reads back as itself.
func TestFormatAmountIsCanonical(t *testing.T) {
	for text, want := range map[string]string{
		"0": "0", "000": "0", "0.00000000": "0", "12": "12", "0012.000": "12", "0.5": "0.5", "0.50": "0.5",
		"1000.00000001": "1000.00000001", "0.1": "0.1", strings.Repeat("9", 18) + ".99999999": strings.Repeat("9", 18) + ".99999999",
	} {
		amount, err := ParseAmount(text)
		if err != nil {
			t.Fatalf("ParseAmount(%q): %v", text, err)
		}
		if got := FormatAmount(amount); got != want {
			t.Errorf("FormatAmount(%q) = %q, want %q", text, got, want)
		}
		if again, err := ParseAmount(FormatAmount(amount)); err != nil || again.Cmp(amount) != 0 {
			t.Errorf("%q does not read back: %v", FormatAmount(amount), err)
		}
	}
	if FormatAmount(nil) != "" {
		t.Error(`FormatAmount(nil) must be "" (a missing amount never hashes like a real one)`)
	}
}

// Every type that carries an amount decodes it through ParseAmount, so these fail at decode, before
// any size or signature check: an exponent-form payment or parameter-vote cost (seconds of event-loop
// time each), checkpoint accounts of "1p9999999" (megabytes per field), and fractions, coprime ones
// included, whose sum's denominator would grow with every credit to the payee.
func TestAmountDecodingRejectsExponentAndOffGridInputs(t *testing.T) {
	for _, testCase := range []struct {
		name, body string
		into       any
	}{
		{"payment, exponent", `{"From":"a","To":"b","Amount":"1e1000000","BlockID":1}`, new(Payment)},
		{"payment, coprime fraction", `{"From":"a","To":"b","Amount":"17/70","BlockID":1}`, new(Payment)},
		{"payment, nine decimals", `{"From":"a","To":"b","Amount":"0.100000001","BlockID":1}`, new(Payment)},
		{"payment, number instead of text", `{"Amount":12}`, new(Payment)},
		{"parameter vote, exponent cost", `{"NewParameters":{"TransactionCost":"1p10000000","StepTimeoutMs":1000},"Sequence":0}`, new(ParameterChange)},
		{"checkpoint accounts, exponent", `{"Accounts":[{"Name":"a","Amount":"1p9999999"}]}`, new(LedgerState)},
		{"reassignment, exponent", `{"Name":"k","PreviousAmount":"1e6","NewAmount":"0"}`, new(Reassignment)},
		{"reassignment, fraction", `{"Name":"k","PreviousAmount":"0","NewAmount":"1/3"}`, new(Reassignment)},
	} {
		if err := json.Unmarshal([]byte(testCase.body), testCase.into); err == nil {
			t.Errorf("%s: decoded without error", testCase.name)
		}
	}
}

// An amount travels as its canonical decimal text wherever its type sits: a value, a pointer, and a
// field or element of another type. A pointer-receiver MarshalJSON would miss the plain value and the
// nested value, which big.Rat would then write as a/b ("25/2").
func TestAmountsTravelAsPlainDecimals(t *testing.T) {
	payment := Payment{From: "a", To: "bob", Amount: rational("12.5"), BlockID: 3}
	account := Account{Name: "n", PublicKey: "k", Amount: rational("0.1")}
	parameters := parametersCosting(rational("0.25"))
	parameters.NewKeyFee, parameters.MaxPaymentAmount, parameters.MaxAccountBalance = rational("1.5"), rational("2.5"), rational("3.5")
	parameterAmounts := []string{`"TransactionCost":"0.25"`, `"NewKeyFee":"1.5"`, `"MaxPaymentAmount":"2.5"`, `"MaxAccountBalance":"3.5"`}
	reassignment := Reassignment{Name: "n", PreviousAmount: rational("1000.00000001"), NewAmount: rational("7")}
	for _, testCase := range []struct {
		name  string
		value any
		want  []string
	}{
		{"a payment value", payment, []string{`"Amount":"12.5"`}},
		{"a payment pointer", &payment, []string{`"Amount":"12.5"`}},
		{"an account value", account, []string{`"Amount":"0.1"`}},
		{"parameters, a value", parameters, parameterAmounts},
		{"a reassignment value", reassignment, []string{`"PreviousAmount":"1000.00000001"`, `"NewAmount":"7"`}},
		{"a block's payments and reassignment", Block{Content: BlockContent{Payments: []Payment{payment}, Reassignment: &reassignment}}, []string{`"Amount":"12.5"`, `"NewAmount":"7"`}},
		{"a parameter change's parameters", ParameterChange{NewParameters: parameters}, parameterAmounts},
		{"a state's accounts and parameters", LedgerState{Accounts: []Account{account}, Parameters: parameters}, []string{`"Amount":"0.1"`, `"TransactionCost":"0.25"`}},
	} {
		encoded, err := json.Marshal(testCase.value)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		for _, want := range testCase.want {
			if !strings.Contains(string(encoded), want) {
				t.Errorf("%s: %s does not hold %s", testCase.name, encoded, want)
			}
		}
		if strings.Contains(string(encoded), "/") {
			t.Errorf("%s: %s writes an amount as a fraction", testCase.name, encoded)
		}
	}
}

// Only a reassignment's two amounts may be null (it leaves a balance as it is). Every other amount is
// required: null, or a missing field, is refused at decode, so no nil amount enters a node from the
// wire. Before, a peer could null a zero balance out of a checkpoint.
func TestRequiredAmountRefusesNull(t *testing.T) {
	defaultParametersJSON := func() string { encoded, _ := json.Marshal(DefaultParameters()); return string(encoded) }()
	if err := json.Unmarshal([]byte(defaultParametersJSON), new(LedgerParameters)); err != nil {
		t.Fatalf("premise: the default parameters decode: %v", err)
	}
	for _, testCase := range []struct {
		name, body string
		into       any
	}{
		{"payment, null", `{"From":"a","To":"b","Amount":null,"BlockID":1}`, new(Payment)},
		{"payment, left out", `{"From":"a","To":"b","BlockID":1}`, new(Payment)},
		{"account, null", `{"Name":"n","Amount":null}`, new(Account)},
		{"account in a state, left out", `{"Accounts":[{"Name":"n"}]}`, new(LedgerState)},
		{"parameters, null cost", `{"TransactionCost":null,"StepTimeoutMs":1000}`, new(LedgerParameters)},
		{"parameters, null new key fee", strings.Replace(defaultParametersJSON, `"NewKeyFee":"1"`, `"NewKeyFee":null`, 1), new(LedgerParameters)},
		{"parameters, largest payment left out", strings.Replace(defaultParametersJSON, `"MaxPaymentAmount":"100000",`, ``, 1), new(LedgerParameters)},
		{"parameters, null largest balance", strings.Replace(defaultParametersJSON, `"MaxAccountBalance":"10000000"`, `"MaxAccountBalance":null`, 1), new(LedgerParameters)},
		{"parameter change, cost left out", `{"NewParameters":{"StepTimeoutMs":1000},"Sequence":0}`, new(ParameterChange)},
	} {
		if err := json.Unmarshal([]byte(testCase.body), testCase.into); err == nil {
			t.Errorf("%s: decoded without error", testCase.name)
		}
	}
	var reassignment Reassignment
	if err := json.Unmarshal([]byte(`{"Name":"k","PreviousAmount":null,"NewAmount":null,"Sequence":1}`), &reassignment); err != nil ||
		reassignment.PreviousAmount != nil || reassignment.NewAmount != nil {
		t.Fatalf("a reassignment's amounts may be null: %v", err)
	}
	if encoded, _ := json.Marshal(reassignment); !strings.Contains(string(encoded), `"PreviousAmount":null`) {
		t.Errorf("an absent optional amount must be written as null: %s", encoded)
	}
}

// Encoding and decoding again gives the same JSON, and a signed payment still verifies after it.
func TestAmountJSONRoundTrips(t *testing.T) {
	payment := signedPayment("payer", "b", "9.9", 5)
	values := []any{
		&payment,
		&Account{Name: "n", PublicKey: "k", Amount: rational("0.1"), Bank: "b", Delegate: "v", Description: "d", Address: "a", Frozen: true, PendingKey: "p", PendingHeight: 9},
		&Reassignment{Name: "n", PreviousAmount: rational("10"), NewAmount: rational("0"), Sequence: 3, Votes: []ReassignmentVote{{Validator: "v", Signature: "s"}}},
		&ParameterChange{NewParameters: parametersCosting(rational("0.25")), Sequence: 1},
	}
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		decoded := reflect.New(reflect.TypeOf(value).Elem()).Interface()
		if err := json.Unmarshal(encoded, decoded); err != nil {
			t.Fatalf("%s does not decode: %v", encoded, err)
		}
		if again, _ := json.Marshal(decoded); string(again) != string(encoded) {
			t.Errorf("round trip changed the JSON:\n  %s\n  %s", encoded, again)
		}
	}
	var decoded Payment
	encoded, _ := json.Marshal(payment)
	if err := json.Unmarshal(encoded, &decoded); err != nil || !VerifyPayment(signing.MLDSA65{}, testNetwork, testSigner("payer").PublicKeyBase64(), decoded) {
		t.Fatalf("a signed payment must still verify after a JSON round trip (err %v)", err)
	}
}

// Signatures cover the canonical decimal, not big.Rat's a/b: a payment of 12.5 is signed as "12.5",
// and so are a ⅔ action's amounts and a parameter vote's transaction cost. A payment signs, in order,
// its payer's name, its payee's name, the amount, the comment, the block and the pin (spec 4.1); a ⅔
// action's vote, the name, the two amounts, the new key, the bank, the freeze and unfreeze marks as 1
// or 0, and the sequence (spec 4.8).
func TestSignedFieldsCarryPlainDecimals(t *testing.T) {
	scheme, network := signing.MLDSA65{}, testNetwork
	key := testSigner("alice").PublicKeyBase64()

	payment := signedPayment("alice", "bob", "12.5", 3)
	paymentFields := [][]byte{[]byte("alice"), []byte("bob"), []byte("12.5"), nil, []byte("3"), nil}
	if !signing.VerifyBase64(scheme, network, key, signing.DomainPayment, payment.Signature, paymentFields...) {
		t.Error(`a payment of 12.5 is not signed over "12.5"`)
	}

	vote, _ := SignReassignmentVote(testSigner("alice"), "alice", Reassignment{Name: "n", PreviousAmount: rational("0.5"), NewAmount: rational("1000"), Bank: "-", Unfreeze: true, Sequence: 2})
	if !signing.VerifyBase64(scheme, network, key, signing.DomainReassignmentVote, vote.Signature,
		[]byte("n"), []byte("0.5"), []byte("1000"), nil, []byte("-"), []byte("0"), []byte("1"), []byte("2")) {
		t.Error(`a ⅔ action's vote from 0.5 to 1000 is not signed over "0.5" and "1000", in the spec 4.8 field order`)
	}

	// The parameter fields feed both the parameter digest and the parameter vote.
	parameters := DefaultParameters()
	parameters.TransactionCost, parameters.NewKeyFee, parameters.MaxPaymentAmount, parameters.MaxAccountBalance = rational("0.25"), rational("1.5"), rational("2.5"), rational("3.5")
	fields := parameters.fields()
	for i, want := range map[int]string{0: "0.25", 5: "1.5", 8: "2.5", 9: "3.5"} {
		if string(fields[i]) != want {
			t.Errorf("parameter field %d is written as %q, want %q", i, fields[i], want)
		}
	}
}
