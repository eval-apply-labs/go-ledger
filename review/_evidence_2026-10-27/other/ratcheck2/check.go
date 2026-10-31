package main

import (
	"fmt"
	"math/big"
	"time"
)

func onePass(amount func(i int) *big.Rat, n int) (time.Duration, int) {
	start := time.Now()
	total := new(big.Rat)
	for i := 0; i < n; i++ {
		total.Add(total, amount(i))
	}
	return time.Since(start), len(total.Denom().String())
}

func main() {
	coprime := func(i int) *big.Rat {
		d := new(big.Int).Lsh(big.NewInt(1), 2600)
		d.Add(d, big.NewInt(int64(1+i)))
		return new(big.Rat).SetFrac(big.NewInt(1), d)
	}
	bounded := func(i int) *big.Rat { return new(big.Rat).SetFrac(big.NewInt(int64(1+i)), big.NewInt(100000000)) }
	for _, n := range []int{100, 1000, 10000, 100000} {
		db, digb := onePass(bounded, n)
		fmt.Printf("bounded-8dp  n=%-7d one outgoingFor pass %-8v  running-sum denom %d digits\n", n, db.Round(time.Microsecond), digb)
	}
	for _, n := range []int{100, 200} {
		dc, digc := onePass(coprime, n)
		fmt.Printf("coprime-783  n=%-7d one outgoingFor pass %-8v  running-sum denom %d digits\n", n, dc.Round(time.Millisecond), digc)
	}
}
