package main

import (
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A second start of a config on the machine that already runs it fails on an address the first start
// holds, its listen address or its gossip listen address, before its node opens, so before it could
// sign: bindThenStart binds both first. Here the node could not even open (its snapshot folder does not
// exist), and the error is still the address's.
func TestStartOnAnAddressInUseFailsBeforeTheNodeOpens(t *testing.T) {
	running, err := net.Listen("tcp", "127.0.0.1:0") // the address the first start holds
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	config, genesis := baseConfig(t)
	nodeConfig, _, _, peers, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatal(err)
	}
	nodeConfig.SnapshotPath = filepath.Join(t.TempDir(), "missing", "snapshot.json") // node.Open refuses it
	for _, addresses := range []struct{ listen, gossipListen string }{{running.Addr().String(), ""}, {"127.0.0.1:0", running.Addr().String()}} {
		_, _, ledgerNode, err := bindThenStart(nodeConfig, addresses.listen, addresses.gossipListen, peers)
		var listenError *net.OpError
		if ledgerNode != nil || !errors.As(err, &listenError) || listenError.Op != "listen" {
			t.Fatalf("a start on listen %s and gossipListen %q, one in use: error %v, want the listen error, before the node opens", addresses.listen, addresses.gossipListen, err)
		}
	}
}

// At the start of a node with a gossip port, a line says that the operator port is on every interface,
// and must not be reachable from outside (review N6), and another that the gossip port is not the
// genesis gossipPort, so only a port mapping in front makes it reachable (review N11). Neither is
// refused.
func TestStartLogsAnOperatorPortOnEveryInterfaceAndAnotherGossipPort(t *testing.T) {
	var logged bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previousOutput)
	const everyInterface, anotherPort = "is on every interface", "is not on the genesis gossipPort 8070"
	for _, test := range []struct {
		listen, gossipListen string
		wantLines            []string
	}{
		{"127.0.0.1:8080", ":8070", nil},
		{"[::1]:8080", "203.0.113.7:8070", nil},
		{"10.0.0.5:8080", "[::]:8070", nil},
		{":8080", ":8070", []string{everyInterface}},
		{"0.0.0.0:8080", ":8070", []string{everyInterface}},
		{"[::]:8080", ":8070", []string{everyInterface}},
		{"127.0.0.1:8080", ":18070", []string{anotherPort}},
		{":8080", "0.0.0.0:18070", []string{everyInterface, anotherPort}},
	} {
		logged.Reset()
		logPortWarnings(test.listen, test.gossipListen, 8070)
		lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
		if logged.Len() == 0 {
			lines = nil
		}
		if len(lines) != len(test.wantLines) {
			t.Errorf("listen %s, gossipListen %s: %d lines, want %d: %q", test.listen, test.gossipListen, len(lines), len(test.wantLines), logged.String())
			continue
		}
		for i, want := range test.wantLines {
			if !strings.Contains(lines[i], want) {
				t.Errorf("listen %s, gossipListen %s: line %q, want one that says %q", test.listen, test.gossipListen, lines[i], want)
			}
		}
	}
}

// With gossipListen set, bindThenStart returns the gossip listener behind the node's allow list (spec
// §5.3): it closes a connection from 127.0.0.1, which no validator publishes and no allow entry holds,
// and lets in one from 127.0.0.2, the allow entry.
func TestTheGossipListenerIsBehindTheAllowList(t *testing.T) {
	config, genesis := baseConfig(t)
	config["gossipListen"], config["allow"] = "127.0.0.1:0", []string{"127.0.0.2"}
	nodeConfig, listen, gossipListen, peers, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatal(err)
	}
	listener, gossipListener, ledgerNode, err := bindThenStart(nodeConfig, listen, gossipListen, peers)
	if err != nil {
		t.Fatal(err)
	}
	defer ledgerNode.Stop()
	defer listener.Close()
	defer gossipListener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if connection, err := gossipListener.Accept(); err == nil {
			accepted <- connection
		}
	}()
	dialFrom := func(sourceAddress string) net.Conn {
		dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(sourceAddress)}, Timeout: 5 * time.Second}
		connection, err := dialer.Dial("tcp", gossipListener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { connection.Close() })
		return connection
	}

	stranger := dialFrom("127.0.0.1")
	stranger.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := stranger.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a connection from 127.0.0.1, which the allow list does not hold, was not closed: %v", err)
	}
	dialFrom("127.0.0.2")
	select {
	case connection := <-accepted:
		if host, _, _ := net.SplitHostPort(connection.RemoteAddr().String()); host != "127.0.0.2" {
			t.Errorf("the gossip listener let in a connection from %s, want the one from 127.0.0.2", host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a connection from 127.0.0.2, an allow entry, was not let in")
	}
}
