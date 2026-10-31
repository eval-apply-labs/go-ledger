package main

import (
	"errors"
	"net"
	"path/filepath"
	"testing"
)

// A second start of a config on the machine that already runs it fails on the listen address, which
// the first start holds, before its node opens, so before it could sign: bindThenStart binds first.
// Here the node could not even open (its snapshot folder does not exist), and the error is still the
// address's.
func TestStartOnAnAddressInUseFailsBeforeTheNodeOpens(t *testing.T) {
	running, err := net.Listen("tcp", "127.0.0.1:0") // the address the first start holds
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	config, genesis := baseConfig(t)
	nodeConfig, _, peers, err := load(writeConfig(t, config, genesis))
	if err != nil {
		t.Fatal(err)
	}
	nodeConfig.SnapshotPath = filepath.Join(t.TempDir(), "missing", "snapshot.json") // node.Open refuses it
	_, ledgerNode, err := bindThenStart(nodeConfig, running.Addr().String(), peers)
	var listenError *net.OpError
	if ledgerNode != nil || !errors.As(err, &listenError) || listenError.Op != "listen" {
		t.Fatalf("a start on an address in use: error %v, want the listen error, before the node opens", err)
	}
}
