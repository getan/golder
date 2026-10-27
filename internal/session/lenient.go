package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/redact"
)

// LoadEntriesLenient is LoadEntries for resume. A line above the scanner's
// cap, or a file above SessionMaxLoadBytes, does not fail the load: the
// oversized line becomes a placeholder message in memory, the file is left
// untouched, and the count of skipped messages is returned so the caller can
// say so. The strict LoadEntries remains for export, import and repair.
//
// A file above SessionMaxLoadBytes is still streamed, not read whole. Its
// normal entries are returned; the read is linear and the caller runs it away
// from the UI.
func (s *Store) LoadEntriesLenient(id string) (SessionHeader, []Entry, int, error) {
	if id == "" {
		return SessionHeader{}, nil, 0, errors.New("session: empty id")
	}
	f, err := os.Open(s.path(id))
	if err != nil {
		return SessionHeader{}, nil, 0, fmt.Errorf("session: open %s: %w", id, err)
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	raw, _, cut, err := readBoundedLine(br, sessionScanBufMax)
	if err != nil && !errors.Is(err, io.EOF) {
		return SessionHeader{}, nil, 0, fmt.Errorf("session: read header: %w", err)
	}
	if cut || len(bytes.TrimSpace(raw)) == 0 {
		return SessionHeader{}, nil, 0, errors.New("session: header line is empty or oversized")
	}
	var header SessionHeader
	if err := json.Unmarshal(bytes.TrimSpace(raw), &header); err != nil {
		return SessionHeader{}, nil, 0, fmt.Errorf("session: parse header: %w", err)
	}
	if header.Version == 0 {
		return SessionHeader{}, nil, 0, errors.New("session: header missing version")
	}
	if header.Version > SchemaVersion {
		return SessionHeader{}, nil, 0, fmt.Errorf("session: file schema version %d newer than supported %d", header.Version, SchemaVersion)
	}
	redactHeader(&header)
	wrapped := header.Version >= 3

	entries := make([]Entry, 0, 256)
	parent := ""
	skipped := 0
	for line := 2; ; line++ {
		raw, total, cut, err := readBoundedLine(br, sessionScanBufMax)
		if errors.Is(err, io.EOF) && len(raw) == 0 {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return SessionHeader{}, nil, 0, fmt.Errorf("session: read line %d: %w", line, err)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var e Entry
		if cut {
			id, parentID, ts := oversizedMeta(raw)
			if id == "" {
				id = newEntryID()
			}
			if parentID == "" {
				parentID = parent
			}
			if ts.IsZero() {
				ts = time.Now().UTC()
			}
			e = Entry{
				ID: id, ParentID: parentID, Timestamp: ts,
				Message: agentcore.UserMessage{
					RoleField: agentcore.RoleUser,
					Content: agentcore.ContentList{agentcore.NewTextContent(
						fmt.Sprintf("[oversized message skipped during load: %d bytes]", total))},
				},
			}
			skipped++
		} else if wrapped {
			if err := json.Unmarshal(bytes.TrimSpace(raw), &e); err != nil {
				return SessionHeader{}, nil, 0, fmt.Errorf("session: parse entry line %d: %w", line, err)
			}
		} else {
			var one agentcore.MessageList
			if err := json.Unmarshal([]byte("["+string(bytes.TrimSpace(raw))+"]"), &one); err != nil {
				return SessionHeader{}, nil, 0, fmt.Errorf("session: parse message line %d: %w", line, err)
			}
			if len(one) != 1 {
				return SessionHeader{}, nil, 0, fmt.Errorf("session: message line %d decoded to %d messages, want 1", line, len(one))
			}
			e = Entry{ID: newEntryID(), ParentID: parent, Timestamp: header.UpdatedAt, Message: one[0]}
		}
		e.Message = redactMessage(redact.SeamSession, e.Message)
		entries = append(entries, e)
		parent = e.ID
	}
	return header, entries, skipped, nil
}
