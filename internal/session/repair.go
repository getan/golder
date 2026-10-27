package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/redact"
)

// RepairReport is what RepairOversized changed.
type RepairReport struct {
	// Lines is how many entry lines the repaired file holds.
	Lines int
	// Omitted is how many oversized lines became placeholder messages.
	Omitted int
	// Backup is the copy of the file as it was before the repair.
	Backup string
}

// RepairOversized rewrites id into a loadable session by streaming it: a line
// over the scanner's 16 MiB cap is replaced by a placeholder message that keeps
// the entry's id, parent and timestamp, while every other line is parsed,
// redacted and written back. The original file is copied to <id>.jsonl.pre-repair
// first, and the replacement is written through a temporary file and renamed.
//
// A file above SessionMaxLoadBytes can be repaired because this path never
// holds the whole file or an oversized line in memory.
func (s *Store) RepairOversized(id string) (RepairReport, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return RepairReport{}, fmt.Errorf("session repair: invalid session id %q", id)
	}
	src := s.path(id)
	in, err := os.Open(src)
	if err != nil {
		return RepairReport{}, fmt.Errorf("session repair: open %s: %w", id, err)
	}
	defer in.Close()

	br := bufio.NewReaderSize(in, 64*1024)
	raw, _, cut, err := readBoundedLine(br, sessionScanBufMax)
	if err != nil {
		return RepairReport{}, fmt.Errorf("session repair: read header: %w", err)
	}
	if cut {
		return RepairReport{}, errors.New("session repair: the header line is oversized")
	}
	var header SessionHeader
	if err := json.Unmarshal(bytes.TrimSpace(raw), &header); err != nil {
		return RepairReport{}, fmt.Errorf("session repair: parse header: %w", err)
	}
	if header.Version < 3 {
		return RepairReport{}, fmt.Errorf("session repair: schema %d is too old; repair supports schema 3 or newer", header.Version)
	}
	redactHeader(&header)

	backup := src + ".pre-repair"
	if err := copyFile(src, backup); err != nil {
		return RepairReport{}, fmt.Errorf("session repair: backup: %w", err)
	}

	tmp := src + ".repair.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return RepairReport{}, fmt.Errorf("session repair: create %s: %w", tmp, err)
	}
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(tmp)
	}
	bw := bufio.NewWriter(out)
	enc := json.NewEncoder(bw)
	if err := enc.Encode(header); err != nil {
		cleanup()
		return RepairReport{}, fmt.Errorf("session repair: encode header: %w", err)
	}

	report := RepairReport{Backup: backup}
	parent := ""
	for line := 2; ; line++ {
		raw, total, cut, err := readBoundedLine(br, sessionScanBufMax)
		if errors.Is(err, io.EOF) && len(raw) == 0 {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			cleanup()
			return RepairReport{}, fmt.Errorf("session repair: read line %d: %w", line, err)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
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
			e := Entry{
				ID: id, ParentID: parentID, Timestamp: ts,
				Message: agentcore.UserMessage{
					RoleField: agentcore.RoleUser,
					Content: agentcore.ContentList{agentcore.NewTextContent(
						fmt.Sprintf("[oversized message omitted during repair: %d bytes]", total))},
				},
			}
			if err := enc.Encode(e); err != nil {
				cleanup()
				return RepairReport{}, fmt.Errorf("session repair: encode line %d: %w", line, err)
			}
			parent = e.ID
			report.Omitted++
		} else {
			var e Entry
			if err := json.Unmarshal(bytes.TrimSpace(raw), &e); err != nil {
				cleanup()
				return RepairReport{}, fmt.Errorf("session repair: parse line %d: %w", line, err)
			}
			e.Message = redactMessage(redact.SeamSession, e.Message)
			if err := enc.Encode(e); err != nil {
				cleanup()
				return RepairReport{}, fmt.Errorf("session repair: encode line %d: %w", line, err)
			}
			parent = e.ID
		}
		report.Lines++
	}
	if err := bw.Flush(); err != nil {
		cleanup()
		return RepairReport{}, fmt.Errorf("session repair: flush: %w", err)
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return RepairReport{}, fmt.Errorf("session repair: sync: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return RepairReport{}, fmt.Errorf("session repair: close: %w", err)
	}
	if err := os.Rename(tmp, src); err != nil {
		_ = os.Remove(tmp)
		return RepairReport{}, fmt.Errorf("session repair: replace %s: %w", id, err)
	}
	return report, nil
}

// readBoundedLine reads one line, keeping at most limit bytes and discarding
// the rest. total counts every byte of the line, so the caller can report what
// was omitted. It returns io.EOF with no data at the end of the file.
func readBoundedLine(br *bufio.Reader, limit int) ([]byte, int64, bool, error) {
	var buf []byte
	var total int64
	for {
		frag, err := br.ReadSlice('\n')
		total += int64(len(frag))
		if len(buf) < limit {
			n := limit - len(buf)
			if n > len(frag) {
				n = len(frag)
			}
			buf = append(buf, frag[:n]...)
		}
		switch {
		case err == nil:
			return buf, total, total > int64(limit), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if total == 0 {
				return nil, 0, false, io.EOF
			}
			return buf, total, total > int64(limit), nil
		default:
			return nil, total, total > int64(limit), err
		}
	}
}

var (
	repairID     = regexp.MustCompile(`"id"\s*:\s*"([^"]+)"`)
	repairParent = regexp.MustCompile(`"parentId"\s*:\s*"([^"]+)"`)
	repairTime   = regexp.MustCompile(`"timestamp"\s*:\s*"([^"]+)"`)
)

// oversizedMeta recovers the tree fields from the readable prefix of an
// oversized line. The fields precede the message in Entry's JSON shape; the
// regexes are the fallback for a file written by another version.
func oversizedMeta(prefix []byte) (id, parent string, ts time.Time) {
	if i := bytes.Index(prefix, []byte(`"message"`)); i > 0 {
		head := bytes.TrimRight(prefix[:i], " \t\r\n,")
		head = append(head, '}')
		var meta struct {
			ID        string    `json:"id"`
			ParentID  string    `json:"parentId"`
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(head, &meta) == nil {
			return meta.ID, meta.ParentID, meta.Timestamp
		}
	}
	if m := repairID.FindSubmatch(prefix); len(m) == 2 {
		id = string(m[1])
	}
	if m := repairParent.FindSubmatch(prefix); len(m) == 2 {
		parent = string(m[1])
	}
	if m := repairTime.FindSubmatch(prefix); len(m) == 2 {
		ts, _ = time.Parse(time.RFC3339Nano, string(m[1]))
	}
	return id, parent, ts
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
