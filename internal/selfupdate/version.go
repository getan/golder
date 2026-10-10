// Package selfupdate provides version discovery and comparison for golder's
// self-update feature (issue #465). It queries the GitHub Releases API for the
// latest published tag of the golder repository and compares it against the
// build-time version injected into the main package, so both `golder update` and
// the interactive startup banner can decide whether a newer release exists.
//
// The build version ("dev" for `go build`/`go run` from source, a real
// vX.Y.Z for goreleaser builds) is not owned by this package; callers pass it
// in. Non-release versions ("", "dev", "unknown") are treated as
// non-comparable so a source build never reports a spurious update.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub "owner/name" whose releases back golder's self-update. It
// matches the release target in .goreleaser.yaml and install.sh.
const Repo = "getan/golder"

// SiteLatestURL is the golder site's latest-release endpoint (golder-site
// repo). It resolves the tag server-side and caches it on Cloudflare's edge:
// no token, no anonymous rate limit, and reachable from mainland China where
// api.github.com often is not. install.sh resolves the version the same way.
const SiteLatestURL = "https://golder-cli.pages.dev/api/latest"

// latestReleaseURL builds the GitHub API endpoint for a repo's latest release.
func latestReleaseURL(repo string) string {
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
}

// release is the subset of the latest-release JSON we consume: the GitHub API
// returns "tag_name", the site endpoint returns "tag".
type release struct {
	TagName string `json:"tag_name"`
	Tag     string `json:"tag"`
}

// IsReleaseVersion reports whether v is a real release version that can be
// compared against a tag. The build defaults ("dev", "unknown") and the empty
// string are not release versions.
func IsReleaseVersion(v string) bool {
	switch strings.TrimSpace(v) {
	case "", "dev", "unknown":
		return false
	default:
		return true
	}
}

// LatestTag resolves repo's latest release tag (e.g. "v0.4.0"). The golder
// site endpoint is tried first (reachable from mainland China, no anonymous
// rate limit, cached on the edge); the GitHub API is the fallback and is the
// path that honors GITHUB_TOKEN. If client is nil a client with a short
// timeout is used.
//
// current is the running build's version, or "" when the caller has none. It
// exists because the site endpoint is edge-cached for a short while: right
// after a release it can still answer with the previous tag, which is exactly
// the moment someone runs `golder update`. When the site's answer is NOT newer
// than current — the answer that would make the caller say "already up to
// date" — the GitHub API is consulted as a cross-check and the newer of the
// two wins. Shelling out to GitHub on that path costs one request in the rare
// case and never happens when the site already reports an upgrade, so the
// common case keeps its single round trip and mainland China (where
// api.github.com is often unreachable) still gets a usable answer: a failed
// cross-check leaves the site's tag standing.
func LatestTag(ctx context.Context, client *http.Client, repo, current string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if repo == Repo {
		siteTag, siteErr := latestTagFromSite(ctx, client)
		if siteErr == nil {
			if siteReportsUpgrade(siteTag, current) {
				return siteTag, nil
			}
			// The site's answer is not an upgrade over what is running — which
			// is also the answer that makes `golder update` print "already up to
			// date", and the tag a source build would install. The site endpoint
			// is edge-cached, so right after a release it can still carry the
			// previous tag; GitHub is the tie-breaker and its failure is not
			// fatal, because mainland China often cannot reach api.github.com
			// and the site's answer is the whole point of asking it first.
			if ghTag, ghErr := latestTagFromGitHub(ctx, client, repo); ghErr == nil {
				return newerTag(siteTag, ghTag), nil
			}
			return siteTag, nil
		}
	}
	return latestTagFromGitHub(ctx, client, repo)
}

// siteReportsUpgrade reports whether the site's tag is strictly newer than the
// running build, which makes it actionable on its own and saves the cross-check
// a request. A non-release current (a source build) can never take this path:
// nothing is an "upgrade" over a version that cannot be compared.
func siteReportsUpgrade(siteTag, current string) bool {
	if !IsReleaseVersion(current) {
		return false
	}
	available, comparable := UpdateAvailable(current, siteTag)
	return comparable && available
}

// newerTag returns the higher of two tags. A tag that cannot be parsed never
// wins: if only one side parses, that side is the answer, since a value the
// caller can act on beats one it cannot.
func newerTag(a, b string) string {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	switch {
	case !aok && !bok:
		return a
	case !aok:
		return b
	case !bok:
		return a
	}
	if compare(bv, av) > 0 {
		return b
	}
	return a
}

// latestTagFromSite queries the golder site's /api/latest endpoint, which
// returns {"tag": "vX.Y.Z"} (or "tag_name", for forward compatibility).
func latestTagFromSite(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SiteLatestURL, nil)
	if err != nil {
		return "", fmt.Errorf("selfupdate: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("selfupdate: query latest release via site: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("selfupdate: site latest endpoint returned %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("selfupdate: decode site latest JSON: %w", err)
	}
	tag := strings.TrimSpace(rel.Tag)
	if tag == "" {
		tag = strings.TrimSpace(rel.TagName)
	}
	if tag == "" {
		return "", fmt.Errorf("selfupdate: site latest endpoint returned empty tag")
	}
	return tag, nil
}

// latestTagFromGitHub queries the GitHub Releases API directly. Anonymous
// callers share a low per-IP rate limit; when the GITHUB_TOKEN environment
// variable is set it is sent as a bearer token to raise that limit.
func latestTagFromGitHub(ctx context.Context, client *http.Client, repo string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL(repo), nil)
	if err != nil {
		return "", fmt.Errorf("selfupdate: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("selfupdate: query latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("selfupdate: GitHub API returned %s", resp.Status)
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("selfupdate: decode release JSON: %w", err)
	}
	tag := strings.TrimSpace(rel.TagName)
	if tag == "" {
		return "", fmt.Errorf("selfupdate: latest release has empty tag_name")
	}
	return tag, nil
}

// UpdateAvailable compares the current build version against a release tag and
// reports whether the tag is strictly newer. The comparable return is false
// when current is not a release version (source builds) or when either value
// cannot be parsed as a version — callers should treat non-comparable as "no
// update to offer" rather than an error.
func UpdateAvailable(current, latest string) (available, comparable bool) {
	if !IsReleaseVersion(current) {
		return false, false
	}
	cur, ok1 := parseVersion(current)
	lat, ok2 := parseVersion(latest)
	if !ok1 || !ok2 {
		return false, false
	}
	return compare(lat, cur) > 0, true
}

// parseVersion parses a semantic-ish version ("v0.4.0", "0.4.0-next") into its
// numeric major/minor/patch, ignoring any leading "v" and any pre-release or
// build suffix after "-" or "+". It reports ok=false when no numeric component
// can be read.
func parseVersion(v string) ([3]int, bool) {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	// Drop pre-release / build metadata: "0.4.0-next" -> "0.4.0".
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// compare returns -1, 0, or 1 as a is less than, equal to, or greater than b.
func compare(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}
