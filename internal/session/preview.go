package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/redact"
)

// Preview returns the text of a session's first non-empty user message, or its
// first non-empty assistant text when the session has no user text. A /resume
// row needs only the opening of a conversation, not the conversation: this
// stops reading at the first user message instead of decoding and redacting a
// whole file that can be many megabytes.
func (s *Store) Preview(id string) (string, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return "", fmt.Errorf("session: open %s: %w", id, err)
	}
	defer f.Close()
	return previewFrom(f)
}

func previewFrom(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, sessionScanBufInit), sessionScanBufMax)

	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", fmt.Errorf("session: read header: %w", err)
		}
		return "", fmt.Errorf("session: empty file (no header)")
	}
	var header SessionHeader
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		return "", fmt.Errorf("session: parse header: %w", err)
	}
	if header.Version == 0 {
		return "", fmt.Errorf("session: header missing version")
	}
	if header.Version > SchemaVersion {
		return "", fmt.Errorf("session: file schema version %d newer than supported %d", header.Version, SchemaVersion)
	}
	wrapped := header.Version >= 3
	fallback := ""
	for line := 2; sc.Scan(); line++ {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		msg, err := decodePreviewMessage(raw, wrapped)
		if err != nil {
			return "", fmt.Errorf("session: parse entry line %d: %w", line, err)
		}
		msg = redactMessage(redact.SeamSession, msg)
		switch m := msg.(type) {
		case agentcore.UserMessage:
			if text := agentcore.ContentToText(m.Content); strings.TrimSpace(text) != "" {
				return text, nil
			}
		case agentcore.AssistantMessage:
			if fallback == "" {
				if text := agentcore.ContentToText(m.Content); strings.TrimSpace(text) != "" {
					fallback = text
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("session: scan: %w", err)
	}
	return fallback, nil
}

func decodePreviewMessage(raw []byte, wrapped bool) (agentcore.Message, error) {
	if wrapped {
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		return e.Message, nil
	}
	var one agentcore.MessageList
	if err := json.Unmarshal([]byte("["+string(raw)+"]"), &one); err != nil {
		return nil, err
	}
	if len(one) != 1 {
		return nil, fmt.Errorf("message line decoded to %d messages, want 1", len(one))
	}
	return one[0], nil
}
