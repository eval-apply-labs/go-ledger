package node

// Debug-only tracing for root-causing TestPaymentGossipReceiptAndReplay (scratch copy only).
// Enabled by GOLEDGER_TRACE=1; writes one line per interesting loop event to the trace sink.

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	traceOn    = os.Getenv("GOLEDGER_TRACE") != ""
	traceMu    sync.Mutex
	traceT0    = time.Now()
	traceNames = map[string]string{}
	traceLines []string
)

// traceReset clears the buffered trace and the name map (call at test start).
func traceReset() {
	traceMu.Lock()
	traceT0 = time.Now()
	traceNames = map[string]string{}
	traceLines = nil
	traceMu.Unlock()
}

func traceName(key, name string) {
	traceMu.Lock()
	traceNames[key] = name
	traceMu.Unlock()
}

func tn(key string) string {
	traceMu.Lock()
	defer traceMu.Unlock()
	if s, ok := traceNames[key]; ok {
		return s
	}
	if len(key) > 6 {
		return key[:6]
	}
	return key
}

func (n *Node) trace(format string, args ...any) {
	if !traceOn {
		return
	}
	name := tn(n.self)
	ms := float64(time.Since(traceT0).Microseconds()) / 1000
	line := fmt.Sprintf("%9.3fms %s h=%d %s", ms, name, n.height, fmt.Sprintf(format, args...))
	traceMu.Lock()
	traceLines = append(traceLines, line)
	traceMu.Unlock()
}

// traceDump returns the buffered trace.
func traceDump() []string {
	traceMu.Lock()
	defer traceMu.Unlock()
	return append([]string(nil), traceLines...)
}
