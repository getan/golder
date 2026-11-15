package adb

import (
	"fmt"
	"io"
	"sync"
)

// Stream is a single ADB service stream. It is intentionally sequential: the
// helper launch path writes its nonce and then reads the helper's short status
// output. The full Phase 2 display session will move this behind a multiplexed
// reader before concurrent streams are allowed.
type Stream struct {
	conn     *Conn
	localID  uint32
	remoteID uint32
	mu       sync.Mutex
	incoming []byte
	closed   bool
}

func (c *Conn) Open(destination string) (*Stream, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("adb connection is closed")
	}
	c.streamMu.Lock()
	localID := c.nextID
	c.nextID++
	c.streamMu.Unlock()
	if err := c.Send(Message{
		Command: CommandOPEN,
		Arg0:    localID,
		Data:    append([]byte(destination), 0),
	}); err != nil {
		return nil, err
	}
	stream := &Stream{conn: c, localID: localID}
	for {
		message, err := c.Read()
		if err != nil {
			return nil, fmt.Errorf("open %q: %w", destination, err)
		}
		switch message.Command {
		case CommandOKAY:
			if message.Arg1 != localID {
				continue
			}
			stream.remoteID = message.Arg0
			return stream, nil
		case CommandWRTE:
			if message.Arg1 != localID {
				continue
			}
			stream.incoming = append(stream.incoming, message.Data...)
			if err := c.Send(Message{Command: CommandOKAY, Arg0: localID, Arg1: message.Arg0}); err != nil {
				return nil, err
			}
		case CommandCLSE:
			return nil, fmt.Errorf("device refused %q", destination)
		}
	}
}

func (s *Stream) Write(data []byte) error {
	if s == nil || s.conn == nil || s.closed {
		return fmt.Errorf("adb stream is closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	max := int(s.conn.MaxData)
	if max <= 0 {
		max = DefaultMaxPayload
	}
	for offset := 0; offset < len(data); {
		end := offset + max
		if end > len(data) {
			end = len(data)
		}
		if err := s.conn.Send(Message{
			Command: CommandWRTE,
			Arg0:    s.localID,
			Arg1:    s.remoteID,
			Data:    data[offset:end],
		}); err != nil {
			return err
		}
		if err := s.awaitAck(); err != nil {
			return err
		}
		offset = end
	}
	return nil
}

func (s *Stream) awaitAck() error {
	for {
		message, err := s.conn.Read()
		if err != nil {
			return err
		}
		switch message.Command {
		case CommandOKAY:
			if message.Arg1 == s.localID {
				return nil
			}
		case CommandWRTE:
			if message.Arg1 == s.localID {
				s.incoming = append(s.incoming, message.Data...)
				if err := s.conn.Send(Message{Command: CommandOKAY, Arg0: s.localID, Arg1: message.Arg0}); err != nil {
					return err
				}
			}
		case CommandCLSE:
			s.closed = true
			return io.EOF
		}
	}
}

func (s *Stream) ReadAll() ([]byte, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("adb stream is closed")
	}
	for {
		message, err := s.conn.Read()
		if err != nil {
			return append([]byte(nil), s.incoming...), err
		}
		switch message.Command {
		case CommandWRTE:
			if message.Arg1 == s.localID {
				s.incoming = append(s.incoming, message.Data...)
				if err := s.conn.Send(Message{Command: CommandOKAY, Arg0: s.localID, Arg1: message.Arg0}); err != nil {
					return nil, err
				}
			}
		case CommandCLSE:
			s.closed = true
			return append([]byte(nil), s.incoming...), nil
		case CommandOKAY:
			// ACK for a previous write; no payload.
		}
	}
}

func (s *Stream) Close() error {
	if s == nil || s.conn == nil || s.closed {
		return nil
	}
	s.closed = true
	return s.conn.Send(Message{Command: CommandCLSE, Arg0: s.localID, Arg1: s.remoteID})
}
