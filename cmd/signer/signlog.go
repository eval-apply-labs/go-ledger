package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// signLog is the signer's record of the votes and proposals it signed (spec §2.4, §2.5): JSON lines, the
// first naming the network, each other one a slot and the fields signed in it, written and fsynced
// before the signature is made (rule 5). It is what rules 3 and 4 read. It keeps the slots of its
// highest height and the one below: when the height moves on, the file is rewritten with only those
// (rule 4 refuses any other slot below the highest height, so they are never asked for again).
type signLog struct {
	path          string
	networkID     string            // the network it signs for, on its first line
	resumeAbove   int64             // the height -resume-above set, on its first line: every slot at or below it is refused
	highestHeight int64             // the highest height it holds a slot at
	slots         map[slot][][]byte // the fields signed in each slot of the highest height and the one below
}

// signLogLine is one line of the sign log: its first line, or a slot signed.
type signLogLine struct {
	NetworkID   string   `json:"networkId,omitempty"`   // the first line's
	ResumeAbove int64    `json:"resumeAbove,omitempty"` // the first line's
	Slot        *slot    `json:"slot,omitempty"`        // a slot signed
	Fields      [][]byte `json:"fields,omitempty"`      // the fields signed in it
}

// openSignLog opens the sign log at path for networkID. With resumeAbove from 0 up, it writes a new one
// whose first line records it, and refuses to where a file exists: the log there knows what was
// signed. With -1, it reads the log, or returns nil where there is none: the signer then refuses every
// vote and proposal (spec §2.6). A log of another network stops the signer. A last line that a crash cut
// short, with no newline, was never fsynced, so its slot was never signed: it is dropped. The log read
// is written back at once, which drops it from the file, and the slots below the height before the
// highest. A path that is a symbolic link is refused: the rewrite would rename a file over the link, not
// over the file it points to, which would be left as it was.
func openSignLog(path, networkID string, resumeAbove int64) (*signLog, error) {
	if pathInfo, err := os.Lstat(path); err == nil && pathInfo.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("the sign log %s is a symbolic link, which the sign log's rewrite would replace with a file: "+
			"give signLogPath as the real path, the one the link points to", path)
	}
	if resumeAbove >= 0 {
		if _, err := os.Lstat(path); err == nil {
			return nil, fmt.Errorf("-resume-above writes a new sign log, and there is one at %s, which knows what was signed: "+
				"start the signer without -resume-above", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		signLog := &signLog{path: path, networkID: networkID, resumeAbove: resumeAbove, slots: map[slot][][]byte{}}
		return signLog, signLog.rewrite(signLog.slots)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte("\n"))
	lines = lines[:len(lines)-1] // what follows the last newline: nothing, or a line cut short
	var firstLine signLogLine
	if len(lines) == 0 || json.Unmarshal(lines[0], &firstLine) != nil || firstLine.NetworkID != networkID {
		return nil, fmt.Errorf("the sign log %s does not begin with this network's id, %q: it is another network's, or no sign log, "+
			"and a new network starts with a new sign log (-resume-above)", path, networkID)
	}
	signLog := &signLog{path: path, networkID: networkID, resumeAbove: firstLine.ResumeAbove, slots: map[slot][][]byte{}}
	for i, line := range lines[1:] {
		var entry signLogLine
		if err := json.Unmarshal(line, &entry); err != nil || entry.Slot == nil {
			return nil, fmt.Errorf("the sign log %s, line %d: not a slot signed", path, i+2)
		}
		if signedFields, signed := signLog.slots[*entry.Slot]; signed && !slices.EqualFunc(signedFields, entry.Fields, bytes.Equal) {
			return nil, fmt.Errorf("the sign log %s, line %d: a second signature for the slot %+v", path, i+2, *entry.Slot)
		}
		signLog.slots[*entry.Slot] = entry.Fields
		signLog.highestHeight = max(signLog.highestHeight, entry.Slot.Height)
	}
	for signedSlot := range signLog.slots {
		if signedSlot.Height < signLog.highestHeight-1 {
			delete(signLog.slots, signedSlot)
		}
	}
	return signLog, signLog.rewrite(signLog.slots)
}

// record writes the signature of fields in requestSlot to the log, fsynced, before it is made (rule 5).
// At the highest height it appends one line. Above it, the height moves on, and it rewrites the log
// with the new slot and those of the height below it, and forgets the rest. If the write fails, the
// slot stays free here, and the log on disk may hold it: the caller stops the signer (takeSlot).
func (signLog *signLog) record(requestSlot slot, fields [][]byte) error {
	if requestSlot.Height <= signLog.highestHeight {
		if err := signLog.appendLine(signLogLine{Slot: &requestSlot, Fields: fields}); err != nil {
			return err
		}
		signLog.slots[requestSlot] = fields
		return nil
	}
	kept := map[slot][][]byte{requestSlot: fields}
	for signedSlot, signedFields := range signLog.slots {
		if signedSlot.Height >= requestSlot.Height-1 {
			kept[signedSlot] = signedFields
		}
	}
	if err := signLog.rewrite(kept); err != nil {
		return err
	}
	signLog.slots, signLog.highestHeight = kept, requestSlot.Height
	return nil
}

// appendLine appends line to the log and fsyncs it. The file must be there: one a person removed while
// the signer runs is not made again without its first line.
func (signLog *signLog) appendLine(line signLogLine) error {
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(signLog.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// rewrite replaces the log with its first line and slots, in one atomic step: a temporary file, fsynced,
// renamed over it, and its folder fsynced, so a crash leaves the old log or the new one, never part.
func (signLog *signLog) rewrite(slots map[slot][][]byte) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer) // one line per value
	encoder.Encode(signLogLine{NetworkID: signLog.networkID, ResumeAbove: signLog.resumeAbove})
	for signedSlot, fields := range slots {
		encoder.Encode(signLogLine{Slot: &signedSlot, Fields: fields})
	}
	temporaryFile, err := os.CreateTemp(filepath.Dir(signLog.path), filepath.Base(signLog.path)+".tmp*") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(temporaryFile.Name()) // nothing left to remove once renamed
	_, err = temporaryFile.Write(buffer.Bytes())
	if err == nil {
		err = temporaryFile.Sync()
	}
	if closeErr := temporaryFile.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporaryFile.Name(), signLog.path)
	}
	if err != nil {
		return err
	}
	folder, err := os.Open(filepath.Dir(signLog.path))
	if err != nil {
		return err
	}
	defer folder.Close()
	return folder.Sync()
}
