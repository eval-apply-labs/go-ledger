package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

func main() {
	// Does SetString accept exponent form?
	for _, s := range []string{"1e1000000", "1e4000000", "1e8000000", "1e9000000", "7e1000000"} {
		r, ok := new(big.Rat).SetString(s)
		fmt.Printf("SetString(%q) ok=%v bits=%v\n", s, ok, func() int {
			if ok {
				return r.Num().BitLen()
			}
			return -1
		}())
	}
	// Exact wire form a client POSTs for a Payment amount, decoded as the server does.
	type P struct{ Amount *big.Rat }
	body := []byte(`{"Amount":"1e4000000"}`)
	var p P
	t0 := time.Now()
	err := json.Unmarshal(body, &p)
	fmt.Printf("\nwire body = %q (%d bytes)\n", body, len(body))
	fmt.Printf("json.Unmarshal err=%v decode=%v  -> rat with %d bits\n", err, time.Since(t0), p.Amount.Num().BitLen())
	// What the loop then pays: RatString (PaymentID/signFields) and json.Marshal (WithinMessageBound).
	t1 := time.Now()
	rs := p.Amount.RatString()
	d1 := time.Since(t1)
	t2 := time.Now()
	mb, _ := json.Marshal(p)
	d2 := time.Since(t2)
	fmt.Printf("RatString=%v (len %d)   json.Marshal=%v (len %d)\n", d1, len(rs), d2, len(mb))
}
