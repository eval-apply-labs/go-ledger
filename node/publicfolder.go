package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/eval-apply-labs/go-ledger/ledger"
)

// The public folder (spec §6.1) holds, under Config.PublicPath, the files the fetcher (cmd/fetcher)
// serves to the public behind a CDN:
//
//   - hdls/status, hdls/checkpoint, hdls/accounts and hdls/governance: the answers of the routes of the
//     same names, byte for byte (jsonAnswer), written after each commit and catch-up, and
//     hdls/governance also when a certificate is newly pooled;
//   - hdls/resolve/name/<name> and hdls/resolve/key/<SHA-256 of the key's bytes, in hex>: the name's
//     record (see publishNameRecord), written when a commit or a catch-up changes it, removed with the
//     name, and the key's removed when the name's key changes;
//   - hdls/bundle, the public bundle (spec §7.7): the consensus part of the node's bundle, written with
//     the files of a commit or a catch-up and at each re-gossip tick (publishBundle).
//
// The loop makes each file's content, as it makes the snapshot's, and a publicFolderWriter writes it off
// the loop, through a temporary file in .incoming and a rename into place, so a reader never sees a
// partial file. The record files stay beside the snapshot (see record.go), where the fetcher reads them.
// The files are public: 0644, in folders of 0755.

// The public folder's folders, relative to Config.PublicPath.
const (
	incomingFolder   = ".incoming" // where a file is written before its rename into place; the fetcher never serves it
	nameRecordFolder = "hdls/resolve/name"
	keyRecordFolder  = "hdls/resolve/key"
)

// nameRecordPath is the path of the record of the name. The name grammar allows only a to z, 0 to 9
// and inner hyphens, so a name is a safe file name.
func nameRecordPath(name string) string { return nameRecordFolder + "/" + name }

// keyRecordPath is the path of the record of the name that holds publicKey: the SHA-256 of the key's
// bytes, in lowercase hex, which a client computes from the key it holds. The ledger holds a key only
// in its base64 (signing.ValidatePublicKeyBase64), so it decodes.
func keyRecordPath(publicKey string) string {
	keyBytes, _ := base64.StdEncoding.DecodeString(publicKey)
	digest := sha256.Sum256(keyBytes)
	return keyRecordFolder + "/" + hex.EncodeToString(digest[:])
}

// openPublicFolder creates the public folder's folders, and writes every file once, from the state the
// node opened at: the folder may be empty, or hold the files of an older state. A record file the folder
// holds as it is now is left alone, so a restart does not write every name's two files again. The
// resolve files of no current name or key are removed, and so is .incoming, with the temporary file of
// any write a crash cut short (see writeFile). Open calls it last, without a public path it does
// nothing, and a folder it can't create or clear is an error.
func (node *Node) openPublicFolder() error {
	if node.config.PublicPath == "" {
		return nil
	}
	if err := os.RemoveAll(filepath.Join(node.config.PublicPath, incomingFolder)); err != nil {
		return fmt.Errorf("public folder: %w", err)
	}
	for _, folder := range []string{incomingFolder, nameRecordFolder, keyRecordFolder} {
		if err := os.MkdirAll(filepath.Join(node.config.PublicPath, folder), 0o755); err != nil {
			return fmt.Errorf("public folder: %w", err)
		}
	}
	node.publicFolder = newPublicFolderWriter(node.config.PublicPath)
	currentPaths := map[string]bool{} // the record paths of the current names and keys
	for _, account := range node.state.Accounts {
		record, _ := jsonAnswer(nameRecordOf(account)) // strings, numbers and booleans: it always marshals
		for _, path := range []string{nameRecordPath(account.Name), keyRecordPath(account.PublicKey)} {
			currentPaths[path] = true
			if held, _ := os.ReadFile(filepath.Join(node.config.PublicPath, path)); !bytes.Equal(held, record) {
				node.publicFolder.write(path, record)
			}
		}
	}
	for _, folder := range []string{nameRecordFolder, keyRecordFolder} {
		entries, err := os.ReadDir(filepath.Join(node.config.PublicPath, folder))
		if err != nil {
			return fmt.Errorf("public folder: %w", err)
		}
		for _, entry := range entries {
			if path := folder + "/" + entry.Name(); !currentPaths[path] {
				node.publicFolder.remove(path)
			}
		}
	}
	node.publishCommittedState()
	return nil
}

// writePublicFolder writes the files a commit or a catch-up changes, given the accounts of the state the
// node left: the record of each name whose record changed, then the files of the committed state
// (publishCommittedState). It finds the changed records by comparing each name's record before and
// after, so it marshals only those (spec §6.1, cost); a balance or a delegate, which the record leaves
// out, changes none. First it removes the files of a name that is gone, and the key's file of a key that
// changed or went, so that a key another name takes in the same block ends with that name's record. It
// runs on the loop, after the commit's record entry is on disk.
func (node *Node) writePublicFolder(previousAccounts []ledger.Account) {
	if node.publicFolder == nil {
		return
	}
	for _, before := range previousAccounts {
		now, found := ledger.FindAccount(node.state.Accounts, before.Name)
		if !found {
			node.publicFolder.remove(nameRecordPath(before.Name))
		}
		if !found || now.PublicKey != before.PublicKey {
			node.publicFolder.remove(keyRecordPath(before.PublicKey))
		}
	}
	for _, account := range node.state.Accounts {
		before, found := ledger.FindAccount(previousAccounts, account.Name)
		if !found || !reflect.DeepEqual(nameRecordOf(before), nameRecordOf(account)) {
			node.publishNameRecord(account)
		}
	}
	node.publishCommittedState()
}

// publishNameRecord queues the record of account's name, under its name and under its key: the fields
// /hdls/resolve answers, without the committed height and state digest of ResolveRecord. Those change at
// every block, so every file would be rewritten at every block; a reader takes them from hdls/status or
// hdls/checkpoint. The record holds nothing else, so every bank of one chain publishes the same bytes
// for a name, whatever the height it learned the record at.
func (node *Node) publishNameRecord(account ledger.Account) {
	record := nameRecordOf(account)
	node.publishAnswer(nameRecordPath(account.Name), record)
	node.publishAnswer(keyRecordPath(account.PublicKey), record)
}

// publishCommittedState queues the files of the committed state: hdls/checkpoint, hdls/accounts,
// hdls/governance, hdls/bundle, and hdls/status last. The fetcher takes a record unit as complete, for
// good, once the status's height in progress has passed it, so a status must never come before the
// record entries of the heights it passes: the record is written on the loop during the commit, before
// this.
func (node *Node) publishCommittedState() {
	snapshot := node.snapshot()
	node.publishAnswer("hdls/checkpoint", snapshot.checkpoint())
	node.publishAnswer("hdls/accounts", snapshot.Accounts)
	node.publishGovernance()
	node.publishBundle()
	node.publishAnswer("hdls/status", node.statusOf(node.state))
}

// publishBundle queues hdls/bundle, the public bundle (spec §7.7): the consensus part of the node's
// bundle, without its pools, at most MaxPublicBundleBytes (see bundle). A bank that the node's gossip
// can't reach pulls it through the fetcher (see pullPublicBundles). The loop makes it at each commit
// and catch-up, and at each re-gossip tick, so it follows the votes of the height in progress. It is
// queued only when its bytes differ from the last ones queued: an idle node's stays the same, tick after
// tick. Without a public folder it does nothing.
func (node *Node) publishBundle() {
	if node.publicFolder == nil {
		return
	}
	if bundle := node.bundle(false, MaxPublicBundleBytes); !bytes.Equal(bundle, node.publishedBundle) {
		node.publishedBundle = bundle
		node.publicFolder.write("hdls/bundle", bundle)
	}
}

// publishGovernance queues hdls/governance, which a commit, a catch-up or a newly pooled certificate
// changes. Without a public folder it does nothing.
func (node *Node) publishGovernance() {
	if node.publicFolder != nil {
		node.publishAnswer("hdls/governance", node.governanceView())
	}
}

// publishAnswer queues the file at path with value's JSON, the bytes a route answers value with. It runs
// on the loop, so the marshal does too, and the writer only writes.
func (node *Node) publishAnswer(path string, value any) {
	content, err := jsonAnswer(value)
	if err != nil {
		log.Printf("public folder: %s not made: %v", path, err)
		return
	}
	node.publicFolder.write(path, content)
}

// publicFolderWriter writes the public folder's files off the event loop, one at a time, in the order
// they were first queued. A file queued again before its write has started keeps its place and takes
// the newer content, so a slow disk costs one write of each file's newest content, never a backlog of
// stale ones. A removal is queued the same way, in order with the writes. The loop queues (write,
// remove); a goroutine, started when the queue gets a file and ending when it is empty, writes.
type publicFolderWriter struct {
	folder  string            // Config.PublicPath
	mutex   sync.Mutex        // guards the fields below: the loop queues, the writing goroutine takes
	queue   []string          // the paths queued and not taken yet, relative to folder, each once, in the order first queued
	pending map[string][]byte // the newest content queued for each path in queue; nil to remove the file
	writing bool              // whether the writing goroutine runs
	idle    *sync.Cond        // signalled when the writing goroutine ends (see waitUntilIdle)
}

func newPublicFolderWriter(folder string) *publicFolderWriter {
	writer := &publicFolderWriter{folder: folder, pending: map[string][]byte{}}
	writer.idle = sync.NewCond(&writer.mutex)
	return writer
}

// write queues content for the file at path, relative to the folder.
func (writer *publicFolderWriter) write(path string, content []byte) {
	if content == nil {
		content = []byte{} // nil would remove the file
	}
	writer.queueChange(path, content)
}

// remove queues the removal of the file at path, relative to the folder.
func (writer *publicFolderWriter) remove(path string) { writer.queueChange(path, nil) }

// queueChange queues content for the file at path, nil to remove it, and starts the writing goroutine
// unless it runs.
func (writer *publicFolderWriter) queueChange(path string, content []byte) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if _, queued := writer.pending[path]; !queued {
		writer.queue = append(writer.queue, path)
	}
	writer.pending[path] = content
	if !writer.writing {
		writer.writing = true
		go writer.writeQueue()
	}
}

// writeQueue writes the queued files one at a time, until the queue is empty. It holds the lock only to
// take the next file, so the loop can queue more meanwhile.
func (writer *publicFolderWriter) writeQueue() {
	writer.mutex.Lock()
	for len(writer.queue) > 0 {
		path := writer.queue[0]
		content := writer.pending[path]
		writer.queue = writer.queue[1:]
		delete(writer.pending, path)
		writer.mutex.Unlock()
		writer.writeFile(path, content)
		writer.mutex.Lock()
	}
	writer.writing = false
	writer.idle.Broadcast()
	writer.mutex.Unlock()
}

// writeFile writes content to the file at path through a temporary file in .incoming, on the same file
// system, and a rename into place, which replaces the file whole; with nil content it removes the file.
// The temporary file has a name of its own (os.CreateTemp), so no other writer can rename it or write
// into it, even one that shares the folder by mistake. A write cut short leaves its temporary file, which
// the next Open removes. It does not wait for the disk (no fsync): Open writes again every file that
// does not hold what it should, so a file a power cut left empty is written again then. A failure is
// logged, and the file is written again when it next changes.
func (writer *publicFolderWriter) writeFile(path string, content []byte) {
	target := filepath.Join(writer.folder, path)
	var err error
	if content == nil {
		if err = os.Remove(target); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		var temporaryFile *os.File
		if temporaryFile, err = os.CreateTemp(filepath.Join(writer.folder, incomingFolder), "next-*"); err == nil {
			_, err = temporaryFile.Write(content)
			err = errors.Join(err, temporaryFile.Chmod(0o644), temporaryFile.Close()) // CreateTemp makes it 0600
			if err == nil {
				err = os.Rename(temporaryFile.Name(), target)
			}
			if err != nil {
				os.Remove(temporaryFile.Name())
			}
		}
	}
	if err != nil {
		log.Printf("public folder: %s: %v", path, err)
	}
}

// waitUntilIdle returns once every file queued so far is written and the writing goroutine has ended.
// Stop waits so, and the tests do. Wait unlocks the mutex while it waits, and locks it again to return.
func (writer *publicFolderWriter) waitUntilIdle() {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	for writer.writing {
		writer.idle.Wait()
	}
}
