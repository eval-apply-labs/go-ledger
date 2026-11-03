//go:build linux

package node

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// fullQueueListener is a listener that drops the first packet of every connection to it until it accepts
// one: Linux drops it while the listener's queue of connections not yet accepted is full. It makes the
// listener's queue as short as it goes, and fills it.
func fullQueueListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	rawListener, err := listener.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	// Called again on a listening socket, listen sets only the length of its queue.
	if controlError := rawListener.Control(func(fileDescriptor uintptr) { err = syscall.Listen(int(fileDescriptor), 0) }); controlError != nil || err != nil {
		t.Fatalf("listen: %v, %v", controlError, err)
	}
	for range 8 {
		connection, err := net.DialTimeout("tcp", listener.Addr().String(), 200*time.Millisecond)
		if err != nil {
			return listener // the queue is full
		}
		t.Cleanup(func() { connection.Close() })
	}
	t.Fatal("premise: the listener's queue never filled, so a connection to it does not hang")
	return nil
}

// A URL that never answers a connection, as on a line that drops packets instead of refusing them,
// holds a push up for about peerDialTimeout, two seconds, before the transport tries the peer's next URL,
// not for the client's 5-second timeout (review N3).
func TestTransportGivesUpAURLThatNeverConnectsAfterTwoSeconds(t *testing.T) {
	silent := fullQueueListener(t)
	var received atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		received.Add(1)
		responseWriter.WriteHeader(http.StatusAccepted)
	}))
	defer peer.Close()
	transport := NewHTTPTransport(nil)
	started := time.Now()
	transport.post([]string{"http://" + silent.Addr().String(), peer.URL}, "/hdls/vote", []byte("{}"))
	if elapsed := time.Since(started); received.Load() != 1 || elapsed < peerDialTimeout/2 || elapsed >= 5*time.Second {
		t.Errorf("the push reached the next URL %d times, after %v; want once, after about %v", received.Load(), elapsed, peerDialTimeout)
	}
}

// A push whose first connection packet is lost reaches the peer all the same (review N15): Linux sends
// the packet again after a second, within peerDialTimeout. Here the peer's listener drops the first
// packet, its queue being full, and starts to accept a moment later, before the packet comes again.
func TestTransportPushSurvivesALostConnectionPacket(t *testing.T) {
	listener := fullQueueListener(t)
	var received atomic.Int32
	peer := &http.Server{Handler: http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		received.Add(1)
		responseWriter.WriteHeader(http.StatusAccepted)
	})}
	defer peer.Close()
	time.AfterFunc(300*time.Millisecond, func() { peer.Serve(listener) }) // accepting empties the queue
	started := time.Now()
	NewHTTPTransport(nil).post([]string{"http://" + listener.Addr().String()}, "/hdls/vote", []byte("{}"))
	if received.Load() != 1 {
		t.Errorf("the push, whose first connection packet was lost, reached the peer %d times after %v; want once", received.Load(), time.Since(started))
	}
}

// Catch-up gives a URL that never answers a connection peerDialTimeout too, not the client's 5 seconds
// (review N8): a bank whose line drops packets would otherwise cost each catch-up tick 5 seconds for each
// of its addresses. Here the node's one bootstrap peer never connects, and its gossip peer holds a
// checkpoint ahead.
func TestCatchUpGivesUpAURLThatNeverConnectsAfterTwoSeconds(t *testing.T) {
	silent := fullQueueListener(t)
	names := []string{"founder", "straggler"}
	checkpoint := checkpointOf(t, 5, testGenesis([]ledger.Account{testAccount("founder", "900"), testAccount("straggler", "50")}, 30*time.Second, 0), validatorsOf(names), nil)
	ahead := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		servePeer(responseWriter, request, checkpoint)
	}))
	defer ahead.Close()
	config := testConfig(staticChain(names...), "straggler", testGenesis([]ledger.Account{testAccount("founder", "1000"), testAccount("straggler", "0")}, 30*time.Second, 0))
	config.Bootstrap = []string{"http://" + silent.Addr().String()}
	straggler := New(config, &peeredRecorder{peers: [][]string{{ahead.URL}}})
	started := time.Now()
	_, _, found := straggler.catchUpCheckpoint()
	if elapsed := time.Since(started); !found || elapsed < peerDialTimeout/2 || elapsed >= 5*time.Second {
		t.Errorf("catch-up found the gossip peer's checkpoint %v, after %v; want found, after about %v", found, elapsed, peerDialTimeout)
	}
}
