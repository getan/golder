package extensions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	setupevent "github.com/getan/golder/extension/setup"
	"github.com/getan/golder/internal/permissions"
)

// Setup is an explicit operator action, never a model tool or handshake side
// effect. Its callback runs as events arrive, while the child keeps polling.
func (m *Manager) Setup(ctx context.Context, ref SourceRef, show func(setupevent.Event) error) error {
	return m.SetupVariant(ctx, ref, "", show)
}

func (m *Manager) SetupVariant(ctx context.Context, ref SourceRef, variant string, show func(setupevent.Event) error) error {
	if m.opts.Permissions != nil && m.opts.Permissions.Mode() == permissions.ReadOnly {
		return errors.New("read-only mode cannot write login state; switch permissions before setup")
	}
	if err := m.Disconnect(ref); err != nil {
		return err
	}
	s, err := m.get(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer m.publish(s)
	argv := s.cfg.Setup
	if variant != "" {
		argv = s.cfg.SetupVariants[variant]
	}
	if m.closed.Load() || m.opts.NoTools || !s.enabled.Load() || len(argv) == 0 {
		return errors.New("setup unavailable: approve and enable a local source with a setup declaration")
	}
	fp, err := fingerprint(ref, s.cfg, m.opts.Cwd)
	if err != nil {
		return err
	}
	if fp == "" || fp != s.approval || fp != s.fingerprint {
		return errors.New("source or dependencies changed; review before setup")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	s.interrupt.Store(&cancel)
	s.state = "setting up"
	defer func() {
		if s.state == "setting up" {
			s.state = "disconnected"
			s.auth = "required"
		}
	}()
	m.publish(s)
	// Child diagnostics may contain authorization values. They deliberately do
	// not enter the normal extension log; only structured safe events are shown.
	diagnostics := &logBuffer{limit: 4096}
	cmd, cleanup, err := prepareCommand(ctx, s.cfg, m.opts.Cwd, argv, diagnostics)
	if err != nil {
		return err
	}
	defer cleanup()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), setupevent.MaxLine)
	count, total := 0, 0
	completed := false
	for scanner.Scan() {
		count++
		total += len(scanner.Bytes())
		if count > 256 || total > 256<<10 {
			cancel()
			_ = cmd.Wait()
			return errors.New("setup event budget exceeded")
		}
		var event setupevent.Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Version != setupevent.Version {
			cancel()
			_ = cmd.Wait()
			return errors.New("invalid setup event (contents withheld)")
		}
		switch event.Type {
		case "authorization":
			if event.ExpiresAt.IsZero() || !event.ExpiresAt.After(time.Now()) || event.ExpiresAt.After(time.Now().Add(20*time.Minute)) {
				cancel()
				_ = cmd.Wait()
				return errors.New("invalid authorization expiry")
			}
			if err = setupevent.AuthorizationURL(event.VerificationURL, s.cfg.SetupOrigins); err != nil {
				cancel()
				_ = cmd.Wait()
				return err
			}
		case "notice":
			event.Message = clean(event.Message, 1024)
		case "complete":
			completed = event.Authenticated
		default:
			cancel()
			_ = cmd.Wait()
			return errors.New("unsupported setup event type")
		}
		// Never forward unknown fields, secrets from raw JSON or control chars.
		event.Service = clean(event.Service, 64)
		if event.Type != "authorization" {
			event.VerificationURL = ""
		}
		if event.Type != "notice" {
			event.Message = ""
		}
		if show != nil {
			if err = show(event); err != nil {
				cancel()
				_ = cmd.Wait()
				return err
			}
		}
	}
	if err = scanner.Err(); err != nil {
		cancel()
		_ = cmd.Wait()
		return errors.New("setup event stream invalid or over limit")
	}
	if err = cmd.Wait(); err != nil {
		s.state, s.auth = "disconnected", "required"
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("setup failed; retry authorization or check CLI/application configuration (raw diagnostics withheld)")
	}
	s.state = "disconnected"
	if !completed {
		return errors.New("setup exited without verified authorization")
	}
	s.auth = "configured"
	s.lastError = ""
	return nil
}

// Reset clears only this source HOME, with its persistent parent lock retained.
// The caller must obtain a separate explicit operator confirmation.
func (m *Manager) Reset(ref SourceRef) error {
	if err := m.Disconnect(ref); err != nil {
		return err
	}
	s, err := m.get(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer m.publish(s)
	if !s.cfg.StateDir {
		return errors.New("source has no persistent state")
	}
	home := sourceHome(ref)
	if err = privateHome(home); err != nil {
		return err
	}
	unlock, err := lockState(filepath.Join(filepath.Dir(home), ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err = os.RemoveAll(home); err != nil {
		return fmt.Errorf("reset source HOME: %w", err)
	}
	s.auth = "required"
	s.lastError = ""
	return nil
}
