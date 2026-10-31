package main

import (
	"fmt"
	"math/big"
	"time"
)

func try(s string) {
	start := time.Now()
	r, ok := new(big.Rat).SetString(s)
	dur := time.Since(start)
	bits := 0
	if ok && r != nil {
		bits = r.Num().BitLen() + r.Denom().BitLen()
	}
	fmt.Printf("%-16q ok=%-5v parse=%-12v bits=%d\n", s, ok, dur.Round(time.Microsecond), bits)
}

func main() {
	for _, s := range []string{
		"1e1000000", "1E1000000", "1e9999999", "1e10000000", "1e10000001",
		"1p9999999", "1p10000000", "1p10000001", "1p1000000000",
		"1e1000000000", "9e1000000", "0x1p10000000", "1e6/1", "123",
	} {
		try(s)
	}
	// RatString cost for the largest accepted magnitude
	for _, s := range []string{"1e1000000", "1p10000000", "1e6", "9e1000000"} {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			fmt.Printf("%-16q NOT ACCEPTED\n", s)
			continue
		}
		start := time.Now()
		str := r.RatString()
		dur := time.Since(start)
		fmt.Printf("%-16q RatString=%-10v len(str)=%d\n", s, dur.Round(time.Millisecond), len(str))
	}
}
