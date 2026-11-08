// fetchnative primes MyGo's verified native-library cache through the GitHub
// Releases API. This avoids depending on github.com's release-download route,
// which can return 504 even when the API and release asset storage are healthy.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const apiRoot = "https://api.github.com"
const maxAssetBytes = 32 << 20

var segment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type nativeFile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Libraries []struct {
		Name  string                `json:"name"`
		Files map[string]nativeFile `json:"files"`
	} `json:"libraries"`
}

type release struct {
	Assets []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"assets"`
}

func main() {
	manifestPath := flag.String("manifest", "", "path to the terminal plugin's mygo-plugin.json")
	platforms := flag.String("platform", "", "comma-separated GOOS/GOARCH targets")
	flag.Parse()
	if err := run(*manifestPath, *platforms); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(manifestPath, platforms string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read native manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse native manifest: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("resolve native cache: %w", err)
	}
	var files map[string]nativeFile
	for _, lib := range m.Libraries {
		if lib.Name == "libghostty-vt" {
			if files != nil {
				return errors.New("duplicate libghostty-vt manifest")
			}
			files = lib.Files
		}
	}
	if len(files) == 0 || platforms == "" {
		return errors.New("native manifest or target platforms missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: safeRedirect}
	for _, platform := range strings.Split(platforms, ",") {
		key := strings.ReplaceAll(platform, "/", "-")
		f, ok := files[key]
		if !ok {
			return fmt.Errorf("native manifest has no target %q", platform)
		}
		if err := fetch(ctx, client, apiRoot, cache, os.Getenv("GH_TOKEN"), f); err != nil {
			return fmt.Errorf("%s: %w", platform, err)
		}
	}
	return nil
}

func safeRedirect(req *http.Request, via []*http.Request) error {
	host := req.URL.Hostname()
	if len(via) > 5 || req.URL.Scheme != "https" || (host != "api.github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
		return errors.New("unexpected GitHub asset redirect")
	}
	if host != "api.github.com" {
		req.Header.Del("Authorization")
	}
	return nil
}

// validate limits the manifest to the upstream MyGo repository and ensures no
// name or checksum can escape MyGo's <cache>/mygo/natives/<sha>/<name> tree.
func validate(f nativeFile) (tag, asset string, err error) {
	decoded, decodeErr := hex.DecodeString(f.SHA256)
	if decodeErr != nil || len(decoded) != sha256.Size || f.SHA256 != strings.ToLower(f.SHA256) || f.Name == "." || f.Name == ".." || !segment.MatchString(f.Name) {
		return "", "", errors.New("invalid native filename or SHA-256")
	}
	u, parseErr := url.Parse(f.URL)
	if parseErr != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("invalid native release URL")
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 7 || parts[0] != "" || parts[1] != "egoist" || parts[2] != "mygo" || parts[3] != "releases" || parts[4] != "download" || !segment.MatchString(parts[5]) || parts[5] == "." || parts[5] == ".." || !segment.MatchString(parts[6]) || parts[6] == "." || parts[6] == ".." {
		return "", "", errors.New("native URL is not a MyGo release asset")
	}
	return parts[5], parts[6], nil
}

func fetch(ctx context.Context, client *http.Client, api, cache, token string, f nativeFile) error {
	tag, asset, err := validate(f)
	if err != nil {
		return err
	}
	dest := filepath.Join(cache, "mygo", "natives", f.SHA256, f.Name)
	if data, err := os.ReadFile(dest); err == nil && verified(data, f.SHA256) {
		return nil
	}
	var r release
	metadata, err := get(ctx, client, api+"/repos/egoist/mygo/releases/tags/"+url.PathEscape(tag), token, "application/vnd.github+json", 4<<20)
	if err != nil {
		return fmt.Errorf("get MyGo release metadata: %w", err)
	}
	if err := json.Unmarshal(metadata, &r); err != nil {
		return fmt.Errorf("parse MyGo release metadata: %w", err)
	}
	var id int64
	for _, a := range r.Assets {
		if a.Name == asset {
			if id != 0 || a.ID <= 0 {
				return errors.New("invalid or duplicate MyGo release asset")
			}
			id = a.ID
		}
	}
	if id == 0 {
		return fmt.Errorf("MyGo release asset %q not found", asset)
	}
	data, err := get(ctx, client, fmt.Sprintf("%s/repos/egoist/mygo/releases/assets/%d", api, id), token, "application/octet-stream", maxAssetBytes)
	if err != nil {
		return fmt.Errorf("download MyGo release asset: %w", err)
	}
	if !verified(data, f.SHA256) {
		return errors.New("native library SHA-256 mismatch")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".native-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows does not replace an existing file with Rename.
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	fmt.Printf("Verified %s (%s)\n", path.Base(f.URL), f.SHA256)
	return nil
}

func verified(data []byte, expected string) bool {
	if len(data) == 0 || len(data) > maxAssetBytes {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == expected
}

func get(ctx context.Context, client *http.Client, endpoint, token, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("GitHub API response exceeds size limit")
	}
	return data, nil
}
