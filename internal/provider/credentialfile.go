package provider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/getan/golder/internal/golderhome"
)

const (
	credentialEnvelopeMagic = "golder-credentials:v1:aes-256-gcm\n"
	credentialKeyBytes      = 32
	maxCredentialFileBytes  = 2 << 20
)

var credentialFileMu sync.Mutex

// SaveCredential atomically creates or replaces one named credential. The
// containing directory is private and the resulting file is always mode 0600.
// Secret values are never included in returned errors.
func SaveCredential(path, name, value string) error {
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if err := validCredentialName(name); err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("credential %q: empty value", name)
	}
	credentialFileMu.Lock()
	defer credentialFileMu.Unlock()
	creds, err := loadCredentialFileOrEmpty(path)
	if err != nil {
		return err
	}
	creds[name] = value
	return writeCredentialFile(path, creds)
}

// DeleteCredential removes one named credential. A missing file or name is a
// no-op so a settings UI can make Remove idempotent.
func DeleteCredential(path, name string) error {
	name = strings.TrimSpace(name)
	if err := validCredentialName(name); err != nil {
		return err
	}
	credentialFileMu.Lock()
	defer credentialFileMu.Unlock()
	creds, err := loadCredentialFileOrEmpty(path)
	if err != nil {
		return err
	}
	if _, ok := creds[name]; !ok {
		return nil
	}
	delete(creds, name)
	return writeCredentialFile(path, creds)
}

// CredentialNames lists stored references without exposing their values.
func CredentialNames(path string) ([]string, error) {
	creds, err := loadCredentialFileOrEmpty(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(creds))
	for name := range creds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func validCredentialName(name string) error {
	if name == "" {
		return fmt.Errorf("empty credential name")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("credential name contains a control character")
	}
	return nil
}

func loadCredentialFileOrEmpty(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("cannot locate the credential file")
	}
	creds, err := LoadCredentialFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	return creds, err
}

func writeCredentialFile(path string, creds map[string]string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credential file: create directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("credential file: protect directory: %w", err)
	}
	plain, err := yaml.Marshal(creds)
	if err != nil {
		return fmt.Errorf("credential file: encode: %w", err)
	}
	raw, err := encryptCredentialFile(path, plain)
	clear(plain)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.yaml")
	if err != nil {
		return fmt.Errorf("credential file: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("credential file: protect temporary file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("credential file: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("credential file: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credential file: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("credential file: commit: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("credential file: protect: %w", err)
	}
	return nil
}

func decodeCredentialFile(path string, raw []byte) (map[string]string, error) {
	if len(raw) > maxCredentialFileBytes {
		return nil, fmt.Errorf("credential file %s: exceeds size limit", path)
	}
	plain := raw
	if strings.HasPrefix(string(raw), credentialEnvelopeMagic) {
		key, err := readCredentialKey(path, false)
		if err != nil {
			return nil, err
		}
		defer clear(key)
		encoded := strings.TrimSpace(string(raw[len(credentialEnvelopeMagic):]))
		payload, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("credential file %s: invalid encrypted payload", path)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("credential file %s: initialize cipher: %w", path, err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("credential file %s: initialize AEAD: %w", path, err)
		}
		if len(payload) < gcm.NonceSize()+gcm.Overhead() {
			return nil, fmt.Errorf("credential file %s: encrypted payload is truncated", path)
		}
		nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
		plain, err = gcm.Open(nil, nonce, ciphertext, []byte(credentialEnvelopeMagic))
		clear(payload)
		if err != nil {
			return nil, fmt.Errorf("credential file %s: authentication failed", path)
		}
		defer clear(plain)
	}
	out := make(map[string]string)
	if err := yaml.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("credential file %s: %w", path, err)
	}
	return out, nil
}

func encryptCredentialFile(path string, plain []byte) ([]byte, error) {
	key, err := readCredentialKey(path, true)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credential file: initialize cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential file: initialize AEAD: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("credential file: generate nonce: %w", err)
	}
	payload := append(nonce, gcm.Seal(nil, nonce, plain, []byte(credentialEnvelopeMagic))...)
	encoded := base64.StdEncoding.EncodeToString(payload)
	clear(payload)
	return []byte(credentialEnvelopeMagic + encoded + "\n"), nil
}

func credentialKeyPath(credentialPath string) string {
	return filepath.Join(filepath.Dir(credentialPath), golderhome.CredentialKeyFile)
}

func readCredentialKey(credentialPath string, create bool) ([]byte, error) {
	keyPath := credentialKeyPath(credentialPath)
	key, err := os.ReadFile(keyPath)
	if err == nil {
		if len(key) != credentialKeyBytes {
			clear(key)
			return nil, fmt.Errorf("credential key %s: invalid length", keyPath)
		}
		return key, nil
	}
	if !os.IsNotExist(err) || !create {
		return nil, fmt.Errorf("credential key %s: %w", keyPath, err)
	}
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("credential key: create directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("credential key: protect directory: %w", err)
	}
	key = make([]byte, credentialKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		clear(key)
		return nil, fmt.Errorf("credential key: generate: %w", err)
	}
	file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		clear(key)
		return readCredentialKey(credentialPath, false)
	}
	if err != nil {
		clear(key)
		return nil, fmt.Errorf("credential key: create: %w", err)
	}
	writeErr := file.Chmod(0o600)
	if writeErr == nil {
		_, writeErr = file.Write(key)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(keyPath)
		clear(key)
		return nil, fmt.Errorf("credential key: write: %w", writeErr)
	}
	return key, nil
}
