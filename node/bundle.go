package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/eval-apply-labs/go-ledger/consensus"
	"github.com/eval-apply-labs/go-ledger/ledger"
)

// A bundle (spec §7.2) carries signed messages where the network can't: a text file that any carrier
// can move, a USB stick, an email, a radio link, a person. It is JSON lines. The first line is its
// header (BundleHeader), and each other line is one message (BundleLine):
//
//	{"bundle":1,"networkId":"village/…","from":"bank-a","heightInProgress":1234}
//	{"type":"vote","body":{…}}
//	{"type":"proposal","body":{…}}
//
// Every message in it is signed on its own, so whoever carries it can lose it or delay it, but can't
// change a message without the change being refused. The operator port exports one (GET /hdls/bundle,
// ExportBundle) and imports one (POST /hdls/bundle, ImportBundle).

// The bounds of a bundle: MaxBundleBytes is the most an export writes and the import route reads, and
// MaxPublicBundleBytes the most the public folder's hdls/bundle holds (spec §7.7). A vote is about 4.7 KB
// of JSON, and a proposal with its block up to 2 MiB.
const (
	MaxBundleBytes       = 64 << 20
	MaxPublicBundleBytes = 8 << 20
)

// BundleVersion is the version of the bundle format, which a header names. An import takes no other.
const BundleVersion = 1

// BundleHeader is a bundle's first line. Bundle is the format's version, BundleVersion. NetworkID is the
// network whose messages it carries: an import refuses the whole bundle of another network. From, the
// exporting node's name, and HeightInProgress, its height in progress, are for the people who carry it;
// no rule reads them.
type BundleHeader struct {
	Bundle           int    `json:"bundle"`
	NetworkID        string `json:"networkId"`
	From             string `json:"from"`
	HeightInProgress int64  `json:"heightInProgress"`
}

// BundleLine is one message of a bundle. Type is the last segment of its gossip route ("proposal",
// "vote", "payment", "update", "name-request", "parameter-vote" or "reassignment-vote", see
// gossipRoute), and Body is exactly the JSON that route takes.
type BundleLine struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

// BundleCounts is what an import did with a bundle's message lines, POST /hdls/bundle's answer:
//
//   - Queued: the proposals and votes, which go to the event loop with no verdict, as on their routes;
//   - Taken: the messages their intake newly pooled;
//   - Refused: the messages their intake refused;
//   - Unreadable: the lines that are no message: not JSON, of an unknown type, or with a body that
//     does not decode or is larger than its route takes.
//
// A message the node already held is in none of them: it was taken before, and costs nothing new.
type BundleCounts struct {
	Queued     int `json:"queued"`
	Taken      int `json:"taken"`
	Refused    int `json:"refused"`
	Unreadable int `json:"unreadable"`
}

// EncodeBundle writes a bundle: the header's line, then one line for each message, in order. It stops
// before the first message whose line would take the bundle past maxBytes, so a bundle cut short ends
// on a whole line. A message of no known type is left out.
func EncodeBundle(header BundleHeader, messages []Message, maxBytes int) []byte {
	bundle, _ := json.Marshal(header) // strings and numbers: it always marshals
	bundle = append(bundle, '\n')
	for _, message := range messages {
		routeName, body := gossipRoute(message)
		encodedBody, err := json.Marshal(body)
		if routeName == "" || err != nil {
			continue
		}
		line, err := json.Marshal(BundleLine{Type: routeName, Body: encodedBody})
		if err != nil {
			continue
		}
		if len(bundle)+len(line)+1 > maxBytes {
			break
		}
		bundle = append(append(bundle, line...), '\n')
	}
	return bundle
}

// ExportBundle is a bundle of everything the node would gossip now (spec §7.3), at most MaxBundleBytes.
// The event loop makes it, since what it holds is the loop's (see bundle). A stopped node gives nil.
func (node *Node) ExportBundle() []byte {
	response := make(chan []byte, 1)
	node.enqueue(event{kind: eventBundleQuery, bundleQuery: response})
	select {
	case bundle := <-response:
		return bundle
	case <-node.quit:
		return nil
	}
}

// bundle is the node's bundle, made on the loop, in this order. First the consensus part: what
// re-gossip sends (consensusMessages), then the pooled certificates, the parameter changes' and then the
// name actions', each in key order, as governanceView lists them. Then, withPools, the pools: name
// requests, updates, then payments, each in pool order. It ends at the last whole line within maxBytes
// (EncodeBundle), so what is cut is what comes last in that order.
func (node *Node) bundle(withPools bool, maxBytes int) []byte {
	messages := node.consensusMessages()
	for _, key := range slices.Sorted(maps.Keys(node.parameterCertificatePool)) {
		messages = append(messages, Message{Type: MessageParameterCertificate, ParameterChange: node.parameterCertificatePool[key]})
	}
	for _, key := range slices.Sorted(maps.Keys(node.reassignmentCertificatePool)) {
		messages = append(messages, Message{Type: MessageReassignmentCertificate, Reassignment: node.reassignmentCertificatePool[key]})
	}
	if withPools {
		for _, request := range node.requests.pending {
			messages = append(messages, Message{Type: MessageNameRequest, NameRequest: &request})
		}
		for _, update := range node.updates.pending {
			messages = append(messages, Message{Type: MessageUpdate, Update: &update})
		}
		for _, payment := range node.mempool.pending {
			messages = append(messages, Message{Type: MessagePayment, Payment: &payment})
		}
	}
	header := BundleHeader{Bundle: BundleVersion, NetworkID: node.config.Chain.NetworkID, From: node.self, HeightInProgress: node.height()}
	return EncodeBundle(header, messages, maxBytes)
}

// ImportBundle hands each message of a bundle to the intake of its gossip route (spec §7.4):
// SubmitProposal, SubmitVote, AdmitPayment, AdmitUpdate, AdmitNameRequest, AdmitParameterCertificate or
// AdmitReassignmentCertificate. They check it as they check gossip, and relay what they take to the
// node's peers as they relay gossip, so one bank's import reaches every bank it can still reach, and a
// message the node already holds is dropped as a second gossip copy is. It returns what became of the
// messages (BundleCounts), and skips a line that is no message. It refuses the whole bundle, and takes
// nothing, when its first line is not a header of this format or names another network.
func (node *Node) ImportBundle(bundle []byte) (BundleCounts, error) {
	return node.importBundle(bundle, nil)
}

// importBundle is ImportBundle, which passes no pulledCheck. The pull of a public bundle passes one (see
// Node.pulledCheck): each vote and proposal must pass it, off the loop, before it is handed to the loop.
// And the pull's import ends at the first line that is unreadable, fails pulledCheck, or that its intake
// refuses, so the bundle's lines after it are not read: a hostile public face costs the node one such
// line, not 8 MiB of them at every pull. A public bundle holds the certificates last (see bundle),
// so an honest one loses only what follows a certificate the node refuses, and takes it at a later pull
// if it can. The operator's import goes on past every such line, and counts it.
func (node *Node) importBundle(bundle []byte, pulledCheck func(Message) bool) (BundleCounts, error) {
	headerLine, messageLines, _ := bytes.Cut(bundle, []byte("\n"))
	var header BundleHeader
	if json.Unmarshal(headerLine, &header) != nil || header.Bundle != BundleVersion {
		return BundleCounts{}, errors.New(`no bundle header: the first line must be {"bundle":1,"networkId":…}`)
	}
	if header.NetworkID != node.config.Chain.NetworkID {
		return BundleCounts{}, fmt.Errorf("another network's bundle: it is for %q, and this node is on %q", header.NetworkID, node.config.Chain.NetworkID)
	}
	var counts BundleCounts
	for _, line := range bytes.Split(messageLines, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue // a blank line, such as the end of the last line
		}
		var bundleLine BundleLine
		var message Message
		readable := json.Unmarshal(line, &bundleLine) == nil
		if readable {
			message, readable = messageOf(bundleLine)
		}
		if !readable {
			counts.Unreadable++
			if pulledCheck != nil {
				return counts, nil // the rest of the bundle is not read
			}
			continue
		}
		if pulledCheck != nil && !pulledCheck(message) {
			return counts, nil
		}
		var newlyPooled bool
		var err error
		switch message.Type {
		case MessageProposal:
			node.SubmitProposal(*message.Proposal)
			counts.Queued++
			continue
		case MessageVote:
			node.SubmitVote(*message.Vote)
			counts.Queued++
			continue
		case MessagePayment:
			newlyPooled, err = node.AdmitPayment(*message.Payment)
		case MessageUpdate:
			newlyPooled, err = node.AdmitUpdate(*message.Update)
		case MessageNameRequest:
			newlyPooled, err = node.AdmitNameRequest(*message.NameRequest)
		case MessageParameterCertificate:
			newlyPooled, err = node.AdmitParameterCertificate(*message.ParameterChange)
		case MessageReassignmentCertificate:
			newlyPooled, err = node.AdmitReassignmentCertificate(*message.Reassignment)
		}
		if err != nil {
			counts.Refused++
			if pulledCheck != nil {
				return counts, nil
			}
		} else if newlyPooled {
			counts.Taken++
		}
	}
	return counts, nil
}

// messageOf is the message a bundle line carries: its body decoded as the route its type names decodes
// its body, within that route's bound (see decode). It reports false for an unknown type, or a body that
// does not decode.
func messageOf(bundleLine BundleLine) (Message, bool) {
	var message Message
	var destination any
	maxBytes := maxBodyBytes
	switch bundleLine.Type {
	case "proposal":
		message.Type, message.Proposal, maxBytes = MessageProposal, &ProposalMessage{}, maxProposalBytes
		destination = message.Proposal
	case "vote":
		message.Type, message.Vote = MessageVote, &consensus.Vote{}
		destination = message.Vote
	case "payment":
		message.Type, message.Payment = MessagePayment, &ledger.Payment{}
		destination = message.Payment
	case "update":
		message.Type, message.Update = MessageUpdate, &ledger.AccountUpdate{}
		destination = message.Update
	case "name-request":
		message.Type, message.NameRequest = MessageNameRequest, &ledger.NameRequest{}
		destination = message.NameRequest
	case "parameter-vote":
		message.Type, message.ParameterChange = MessageParameterCertificate, &ledger.ParameterChange{}
		destination = message.ParameterChange
	case "reassignment-vote":
		message.Type, message.Reassignment = MessageReassignmentCertificate, &ledger.Reassignment{}
		destination = message.Reassignment
	default:
		return Message{}, false
	}
	return message, len(bundleLine.Body) <= maxBytes && json.Unmarshal(bundleLine.Body, destination) == nil
}
