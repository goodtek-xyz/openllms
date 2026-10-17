package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// `llms update` replaces the running binary with the gateway's dist build:
//  1. the binary's own path: os.Executable ("the path name for the executable that started the current process"),
//     symlinks resolved, so the file that actually runs is the one replaced;
//  2. {api_base}/dist/llms_<os>_<arch> is downloaded next to it and checked against {api_base}/dist/SHA256SUMS —
//     no listed checksum, no install;
//  3. the new file runs `version` before it is moved over the old one with rename(2), atomic within one directory
//     (POSIX rename), so a failed update leaves the old binary as it was;
//  4. when PATH finds another llms first, both paths are printed — that one is what `llms` runs.
//
// The source is the gateway this CLI talks to, never another one: the CLI matches its gateway. A gateway without a
// dist (self-hosted without CLI builds) gets a pointer to building from source instead.

// errNoDist means the gateway publishes no CLI builds.
var errNoDist = errors.New("this gateway publishes no CLI builds (/dist)")

func distAssetName(goos, goarch string) string { return "llms_" + goos + "_" + goarch }

// parseSHA256SUMS reads `sha256sum` / `shasum -a 256` output: "<64 hex>  <name>" (binary mode "<hex> *<name>").
func parseSHA256SUMS(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("SHA256SUMS: unexpected line %q", line)
		}
		if _, err := hex.DecodeString(f[0]); err != nil {
			return nil, fmt.Errorf("SHA256SUMS: unexpected line %q", line)
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return out, sc.Err()
}

func distGet(ctx context.Context, base, name string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/dist/"+name, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return nil, errNoDist
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("dist %s: HTTP %d", name, res.StatusCode)
	}
	return res, nil
}

// selfPath is the file the running process was started from, symlinks resolved.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// replaceBinary downloads asset from base into the directory of target, checks it against want (hex SHA-256), runs
// `<new> version`, and renames it over target. On any failure target is untouched and the temp file removed.
func replaceBinary(ctx context.Context, base, asset, want, target string) (newVersion string, err error) {
	res, err := distGet(ctx, base, asset)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".llms-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot write next to %s (%w) — run with permission for that directory, or reinstall", target, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(tmp, h), res.Body); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("checksum mismatch for %s: got %s, SHA256SUMS lists %s — not installed", asset, got, want)
	}
	mode := os.FileMode(0o755)
	if st, serr := os.Stat(target); serr == nil {
		mode = st.Mode().Perm() | 0o111
	}
	if err = os.Chmod(tmp.Name(), mode); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, tmp.Name(), "version").Output()
	if err != nil {
		return "", fmt.Errorf("new binary does not run (%w) — not installed", err)
	}
	newVersion = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if err = os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	return newVersion, nil
}

// shadowingLLMS is the llms that PATH runs when it is not self ("" when it is, or none is found).
func shadowingLLMS(self string) string {
	p, err := exec.LookPath("llms")
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if p == self {
		return ""
	}
	return p
}

func cmdSelfUpdate() *cobra.Command {
	var check bool
	c := &cobra.Command{
		Use:          "update",
		Short:        "Update this llms CLI from the gateway (checksum-verified, in place)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			base := apiBase()
			self, err := selfPath()
			if err != nil {
				return err
			}
			latest, err := fetchLatestVersion(ctx, base)
			if err != nil {
				return noDistHelp(base, err)
			}
			fmt.Printf("installed %s at %s\nlatest    %s (%s)\n", Version, self, latest, base)
			if !isNewerVersion(Version, latest) {
				fmt.Println("Up to date.")
				warnShadow(self)
				return nil
			}
			if check {
				fmt.Println("Update available: llms update")
				return nil
			}
			res, err := distGet(ctx, base, "SHA256SUMS")
			if err != nil {
				return noDistHelp(base, err)
			}
			sums, err := parseSHA256SUMS(res.Body)
			res.Body.Close()
			if err != nil {
				return err
			}
			asset := distAssetName(runtime.GOOS, runtime.GOARCH)
			want, ok := sums[asset]
			if !ok {
				return fmt.Errorf("SHA256SUMS on %s lists no %s — not installed", base, asset)
			}
			nv, err := replaceBinary(ctx, base, asset, want, self)
			if err != nil {
				return err
			}
			fmt.Printf("Updated %s → %s\n", self, nv)
			warnShadow(self)
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "Only report whether an update is available")
	return c
}

func noDistHelp(base string, err error) error {
	if errors.Is(err, errNoDist) {
		return fmt.Errorf("%s publishes no CLI builds (/dist). Build from source instead:\n"+
			"  go install github.com/goodtek-xyz/openllms/cmd/llms@latest\n"+
			"or build the gateway image with CLI_DIST=1 so it serves /dist", base)
	}
	return err
}

func warnShadow(self string) {
	if other := shadowingLLMS(self); other != "" {
		fmt.Fprintf(os.Stderr, "\nNote: `llms` on your PATH is %s, not %s.\n"+
			"  Remove the other one, or put %s earlier in PATH.\n", other, self, filepath.Dir(self))
	}
}
