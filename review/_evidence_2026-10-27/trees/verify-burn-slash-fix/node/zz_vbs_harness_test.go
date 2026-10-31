package node

// Verifier (burn-slash group) harness: an in-memory full-mesh bus with per-link filtering and a
// broadcast tap, plus a "crashable" transport that kills a node's I/O at an exact outgoing message.
// A crash is then completed by Stop() and a real restart is New(sameConfig) — which reloads the
// node's own snapshot file exactly as cmd/server does after a reboot.

import (
	"sync"
	"sync/atomic"

	"github.com/eval-apply-labs/go-ledger/consensus"
)

type vbus struct {
	mu       sync.Mutex
	handlers map[string]func(Message)
	filter   func(from, to string, m Message) bool // false = drop on this link
	tapMu    sync.Mutex
	tapped   []tapped
}

type tapped struct {
	from string
	msg  Message
}

func newVBus() *vbus { return &vbus{handlers: map[string]func(Message){}} }

func (b *vbus) setNode(key string, nd *Node) {
	b.mu.Lock()
	b.handlers[key] = func(m Message) { deliverToNode(nd, m) }
	b.mu.Unlock()
}

func (b *vbus) setHandler(key string, h func(Message)) {
	b.mu.Lock()
	b.handlers[key] = h
	b.mu.Unlock()
}

func (b *vbus) setFilter(f func(from, to string, m Message) bool) {
	b.mu.Lock()
	b.filter = f
	b.mu.Unlock()
}

func (b *vbus) Broadcast(from string, m Message) {
	b.mu.Lock()
	hs := make(map[string]func(Message), len(b.handlers))
	for k, v := range b.handlers {
		hs[k] = v
	}
	filter := b.filter
	b.mu.Unlock()
	b.tapMu.Lock()
	b.tapped = append(b.tapped, tapped{from: from, msg: copyMsg(m)})
	b.tapMu.Unlock()
	for k, h := range hs {
		if k == from {
			continue
		}
		if filter != nil && !filter(from, k, m) {
			continue
		}
		h := h
		mm := copyMsg(m)
		go h(mm)
	}
}

// votesBy returns every vote `key` broadcast (own or relayed — relays carry the original signer).
func (b *vbus) votesBy(key string, height int64) []consensus.Vote {
	b.tapMu.Lock()
	defer b.tapMu.Unlock()
	var out []consensus.Vote
	seen := map[string]bool{}
	for _, t := range b.tapped {
		if t.msg.Type == MsgVote && t.msg.Vote != nil && t.msg.Vote.Validator == key && t.msg.Vote.Height == height {
			k := t.msg.Vote.Signature
			if !seen[k] {
				seen[k] = true
				out = append(out, *t.msg.Vote)
			}
		}
	}
	return out
}

func (b *vbus) proposalsFrom(from string) []ProposalMsg {
	b.tapMu.Lock()
	defer b.tapMu.Unlock()
	var out []ProposalMsg
	for _, t := range b.tapped {
		if t.from == from && t.msg.Type == MsgProposal && t.msg.Proposal != nil {
			out = append(out, *t.msg.Proposal)
		}
	}
	return out
}

func copyMsg(m Message) Message {
	c := m
	if m.Vote != nil {
		v := *m.Vote
		c.Vote = &v
	}
	if m.Proposal != nil {
		p := *m.Proposal
		c.Proposal = &p
	}
	if m.Payment != nil {
		p := *m.Payment
		c.Payment = &p
	}
	return c
}

// crashTransport wraps the bus for ONE node. When `trigger` matches an outgoing message, that message
// is still delivered (it left the NIC before the power cut) and from then on every outgoing message
// is dropped and `crashed` reports true (the bus filter also drops inbound to the crashed key).
type crashTransport struct {
	bus     *vbus
	trigger func(m Message) bool
	crashed atomic.Bool
	fired   chan struct{}
	once    sync.Once
}

func newCrashTransport(b *vbus, trigger func(m Message) bool) *crashTransport {
	return &crashTransport{bus: b, trigger: trigger, fired: make(chan struct{})}
}

func (c *crashTransport) Broadcast(from string, m Message) {
	if c.crashed.Load() {
		return
	}
	if c.trigger != nil && c.trigger(m) {
		c.crashed.Store(true) // set BEFORE delivery so nothing sent after this can race past it
		c.bus.Broadcast(from, m)
		c.once.Do(func() { close(c.fired) })
		return
	}
	c.bus.Broadcast(from, m)
}
