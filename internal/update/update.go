// Package update keeps an installed memory current. When any memory command
// runs, at most once a day, it starts a detached background process that asks
// GitHub for the latest release and, if it is newer, downloads it, checks it
// against the release's SHA256SUMS, replaces the binary, and has the new
// binary install itself again for the agents memory is installed for — so the
// skill and the hooks change with it. The command that started the check does
// not wait for it.
//
// A build from source (Version "dev") never updates itself, nor does one run
// with MEMORY_AUTO_UPDATE=off.
package update

import (
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
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Version is this build's release tag, set when a release is built:
// -ldflags "-X memory/internal/update.Version=v0.3.0".
var Version = "dev"

// Releases is where releases are published; a variable so tests can serve one.
var Releases = "https://github.com/river-ranx/Personal-Memory-Plugin/releases"

// interval is how often the background check runs, at most.
const interval = 24 * time.Hour

// stampName is the file, in memory's home, whose time is the last check and
// whose text is what came of it.
const stampName = "update-check"

// Due starts the background check if one is due: not a dev build, not
// switched off, none in the last day. It costs the command one stat when
// nothing is due, and never fails it.
func Due(home string, getenv func(string) string) {
	if Version == "dev" || off(getenv("MEMORY_AUTO_UPDATE")) {
		return
	}
	stamp := filepath.Join(home, stampName)
	if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < interval {
		return
	}
	// Marked before starting, so the commands that follow in the next moments
	// do not each start one.
	if os.MkdirAll(home, 0o700) != nil || os.WriteFile(stamp, []byte("checking\n"), 0o600) != nil {
		return
	}
	start()
}

// start runs `memory update --background` from this binary, detached: its own
// session and no inherited output, so the agent that ran the hook is neither
// kept waiting on it nor ends it. A variable so tests see it without running it.
var start = func() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "update", "--background")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// Run checks for a newer release and installs it over exe. It says what it did
// on out, and notes it in the stamp for whoever looks later. reinstall is run
// with the new binary's path once it is in place.
func Run(home, exe string, out io.Writer, reinstall func(newExe string) error) error {
	result, err := run(exe, reinstall)
	note := result
	if err != nil {
		note = "failed: " + err.Error()
	}
	if os.MkdirAll(home, 0o700) == nil {
		_ = os.WriteFile(filepath.Join(home, stampName), []byte(time.Now().Format(time.RFC3339)+" "+note+"\n"), 0o600)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, result)
	return nil
}

func run(exe string, reinstall func(string) error) (string, error) {
	if Version == "dev" {
		return "", errors.New("this is a build from source (version dev); it does not update itself — reinstall with the curl command, or build again")
	}
	latest, err := latestTag()
	if err != nil {
		return "", err
	}
	if !newer(latest, Version) {
		return "memory " + Version + " is the latest.", nil
	}
	asset := "memory-" + runtime.GOOS + "-" + runtime.GOARCH
	base := Releases + "/download/" + latest
	sums, err := fetch(base + "/SHA256SUMS")
	if err != nil {
		return "", err
	}
	want := checksum(string(sums), asset)
	if want == "" {
		return "", fmt.Errorf("%s has no checksum for %s", latest, asset)
	}
	binary, err := fetch(base + "/" + asset)
	if err != nil {
		return "", err
	}
	if got := sha256.Sum256(binary); hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("%s/%s does not match its checksum", latest, asset)
	}
	if err := replace(exe, binary); err != nil {
		return "", err
	}
	if err := reinstall(exe); err != nil {
		return "", fmt.Errorf("updated to %s, but setting up the agents again failed: %w", latest, err)
	}
	return "Updated memory " + Version + " → " + latest + ".", nil
}

// latestTag is the tag of the latest release, read from where GitHub's
// releases/latest redirects (…/releases/tag/v0.3.0): no API call, so no
// rate limit for a machine without a token.
func latestTag() (string, error) {
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Head(Releases + "/latest")
	if err != nil {
		return "", fmt.Errorf("could not ask for the latest release: %w", err)
	}
	resp.Body.Close()
	_, tag, found := strings.Cut(resp.Header.Get("Location"), "/releases/tag/")
	if !found || tag == "" {
		return "", fmt.Errorf("could not read the latest release (%s)", resp.Status)
	}
	return tag, nil
}

func fetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("could not download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not download %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// checksum is the hash SHA256SUMS lists for name.
func checksum(sums, name string) string {
	for _, line := range strings.Split(sums, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			return f[0]
		}
	}
	return ""
}

// replace puts binary at exe: written beside it, then renamed over it, so a
// memory running from the old file keeps it and no one sees half a file.
func replace(exe string, binary []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".memory-update-*")
	if err != nil {
		return fmt.Errorf("cannot write beside %s: %w", exe, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}

// newer is whether release tag a is after b; both are vMAJOR.MINOR.PATCH.
func newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(tag string) []int {
	fields := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(fields) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

func off(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}
