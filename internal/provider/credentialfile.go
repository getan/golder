package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

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
	raw, err := yaml.Marshal(creds)
	if err != nil {
		return fmt.Errorf("credential file: encode: %w", err)
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
