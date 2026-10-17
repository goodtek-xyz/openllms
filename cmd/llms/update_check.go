package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Update notice: compare this binary's Version against the gateway dist
// ({api_base}/dist/VERSION) at most once per updateCheckTTL, and print a
// one-line hint on stderr when the dist build is newer.

const (
	updateCheckTTL     = 24 * time.Hour
	updateCheckTimeout = 1500 * time.Millisecond
)

type updateCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

func updateCachePath() string {
	return filepath.Join(filepath.Dir(credsPath()), "update-check.json")
}

func loadUpdateCache() (updateCache, bool) {
	b, err := os.ReadFile(updateCachePath())
	if err != nil {
		return updateCache{}, false
	}
	var c updateCache
	if json.Unmarshal(b, &c) != nil {
		return updateCache{}, false
	}
	return c, true
}

func saveUpdateCache(c updateCache) {
	path := updateCachePath()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(path, b, 0o600)
}

// describeRe matches `git describe --tags` output: v0.1.1, v0.1.1-118-g0703955, optional -dirty.
var describeRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(\d+)-g[0-9a-f]+)?(?:-dirty)?$`)

func parseDescribe(v string) ([4]int, bool) {
	m := describeRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [4]int{}, false
	}
	var out [4]int
	for i := 0; i < 4; i++ {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [4]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// isNewerVersion reports whether latest is strictly newer than current.
// Unparseable versions (dev builds, bare commits) never trigger a notice.
func isNewerVersion(current, latest string) bool {
	cur, ok1 := parseDescribe(current)
	lat, ok2 := parseDescribe(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range cur {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

func updateCheckDisabled() bool {
	if os.Getenv("LLMS_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" {
		return true
	}
	if _, ok := parseDescribe(Version); !ok {
		return true
	}
	fi, err := os.Stderr.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return true
	}
	return false
}

// fetchLatestVersion reads {base}/dist/VERSION within ctx (the background notice bounds it; `llms update` does not).
func fetchLatestVersion(ctx context.Context, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/dist/VERSION", nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return "", errNoDist
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dist VERSION: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 256))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if _, ok := parseDescribe(v); !ok {
		return "", fmt.Errorf("dist VERSION: unexpected %q", v)
	}
	return v, nil
}

// latestVersion returns the dist version, using the cache unless it is stale
// or force is set. Network errors fall back to the cached value.
func latestVersion(ctx context.Context, base string, now time.Time, force bool) string {
	cached, ok := loadUpdateCache()
	if ok && !force && now.Sub(cached.CheckedAt) < updateCheckTTL {
		return cached.Latest
	}
	fctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	v, err := fetchLatestVersion(fctx, base)
	cancel()
	if err != nil {
		// Record the attempt so an offline gateway is not retried on every command.
		saveUpdateCache(updateCache{CheckedAt: now, Latest: cached.Latest})
		return cached.Latest
	}
	saveUpdateCache(updateCache{CheckedAt: now, Latest: v})
	return v
}

func updateNotice(current, latest, base string) string {
	if !isNewerVersion(current, latest) {
		return ""
	}
	return fmt.Sprintf(
		"\nA newer llms CLI is available: %s (installed %s)\n  Update: llms update  (or: curl -fsSL %s/install.sh | bash)\n",
		latest, current, strings.TrimRight(base, "/"),
	)
}

// maybePrintUpdateNotice runs after a command; silent on any failure.
func maybePrintUpdateNotice(ctx context.Context) {
	if updateCheckDisabled() {
		return
	}
	base := apiBase()
	if msg := updateNotice(Version, latestVersion(ctx, base, time.Now(), false), base); msg != "" {
		fmt.Fprint(os.Stderr, msg)
	}
}
