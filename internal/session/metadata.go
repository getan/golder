package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Rename gives a stored session a person-facing title. An empty title clears
// the override so front-ends derive the title from the transcript again.
func (s *Store) Rename(id, title string) (SessionHeader, error) {
	if err := ValidateID(id); err != nil {
		return SessionHeader{}, err
	}
	header, entries, err := s.LoadEntries(id)
	if err != nil {
		return SessionHeader{}, err
	}
	header.DisplayName = strings.TrimSpace(title)
	if r := []rune(header.DisplayName); len(r) > 120 {
		header.DisplayName = string(r[:120])
	}
	if err := s.writeEntries(header, entries); err != nil {
		return SessionHeader{}, err
	}
	return header, nil
}

// SetArchived marks a stored session archived or active without changing its
// conversational UpdatedAt ordering.
func (s *Store) SetArchived(id string, archived bool) (SessionHeader, error) {
	if err := ValidateID(id); err != nil {
		return SessionHeader{}, err
	}
	header, entries, err := s.LoadEntries(id)
	if err != nil {
		return SessionHeader{}, err
	}
	if archived {
		now := time.Now().UTC()
		header.ArchivedAt = &now
	} else {
		header.ArchivedAt = nil
	}
	if err := s.writeEntries(header, entries); err != nil {
		return SessionHeader{}, err
	}
	return header, nil
}

// Delete permanently removes one stored session. The caller owns process-level
// writer coordination; Store validates the id and removes only its JSONL file.
func (s *Store) Delete(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	err := os.Remove(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session %s does not exist", id)
	}
	if err != nil {
		return fmt.Errorf("session: delete %s: %w", id, err)
	}
	return nil
}

// ValidateID rejects an empty or path-shaped session id before a caller creates
// a lock/export path for it.
func ValidateID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return fmt.Errorf("session: invalid session id")
	}
	return nil
}
