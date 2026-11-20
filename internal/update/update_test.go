package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewerComparesReleaseTags(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.3.0", "v0.2.0", true},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.1.9", "v0.2.0", false},
		{"v0.3.0", "dev", false},
		{"latest", "v0.2.0", false},
		{"v0.3", "v0.2.0", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Fatalf("newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// release serves a GitHub-like releases page: latest redirects to tag, and
// tag's assets are the given binary and its SHA256SUMS (with listed as the
// checksum written there).
func release(t *testing.T, tag string, binary []byte, listed string) *int {
	t.Helper()
	asset := "memory-" + runtime.GOOS + "-" + runtime.GOARCH
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
		case "/releases/download/" + tag + "/SHA256SUMS":
			w.Write([]byte(listed + "  " + asset + "\nabc  memory-other-os\n"))
		case "/releases/download/" + tag + "/" + asset:
			downloads++
			w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old, oldVersion := Releases, Version
	Releases, Version = srv.URL+"/releases", "v0.2.0"
	t.Cleanup(func() { Releases, Version = old, oldVersion })
	return &downloads
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func exeAt(t *testing.T) (home, exe string) {
	dir := t.TempDir()
	exe = filepath.Join(dir, "bin", "memory")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("old build"), 0o755)
	return filepath.Join(dir, ".memory"), exe
}

// A newer release is downloaded, checked, put in place of the binary, and the
// new binary sets up the agents again; the stamp says what happened.
func TestANewerReleaseReplacesTheBinaryAndSetsUpAgain(t *testing.T) {
	binary := []byte("new build")
	release(t, "v0.3.0", binary, sum(binary))
	home, exe := exeAt(t)
	var reinstalled string
	var out bytes.Buffer
	if err := Run(home, exe, &out, func(newExe string) error { reinstalled = newExe; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new build" {
		t.Fatalf("binary is %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode())
	}
	if reinstalled != exe || !strings.Contains(out.String(), "v0.2.0 → v0.3.0") {
		t.Fatalf("reinstalled %q, said %q", reinstalled, out.String())
	}
	if stamp, _ := os.ReadFile(filepath.Join(home, stampName)); !strings.Contains(string(stamp), "Updated") {
		t.Fatalf("stamp %q", stamp)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".memory-update-*")); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

// A download that does not match its checksum is not installed.
func TestADownloadThatDoesNotMatchIsRefused(t *testing.T) {
	release(t, "v0.3.0", []byte("tampered"), sum([]byte("new build")))
	home, exe := exeAt(t)
	err := Run(home, exe, &bytes.Buffer{}, func(string) error { t.Fatal("set up again after a refused download"); return nil })
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old build" {
		t.Fatalf("binary is %q", got)
	}
	if stamp, _ := os.ReadFile(filepath.Join(home, stampName)); !strings.Contains(string(stamp), "failed") {
		t.Fatalf("stamp %q", stamp)
	}
}

// The same release, or an older one, downloads nothing.
func TestTheLatestReleaseDownloadsNothing(t *testing.T) {
	for _, tag := range []string{"v0.2.0", "v0.1.5"} {
		downloads := release(t, tag, []byte("x"), sum([]byte("x")))
		home, exe := exeAt(t)
		var out bytes.Buffer
		if err := Run(home, exe, &out, func(string) error { t.Fatal("set up again"); return nil }); err != nil {
			t.Fatal(err)
		}
		if *downloads != 0 || !strings.Contains(out.String(), "is the latest") {
			t.Fatalf("%s: %d downloads, said %q", tag, *downloads, out.String())
		}
	}
}

// A build from source never updates itself.
func TestADevBuildDoesNotUpdate(t *testing.T) {
	release(t, "v0.3.0", []byte("new build"), sum([]byte("new build")))
	Version = "dev"
	home, exe := exeAt(t)
	if err := Run(home, exe, &bytes.Buffer{}, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "dev") {
		t.Fatalf("err %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old build" {
		t.Fatal("a dev build was replaced")
	}
}

// The background check starts at most once a day, never for a dev build, and
// not when switched off.
func TestTheBackgroundCheckIsDueOnceADay(t *testing.T) {
	started := 0
	oldStart, oldVersion := start, Version
	start, Version = func() { started++ }, "v0.2.0"
	t.Cleanup(func() { start, Version = oldStart, oldVersion })
	home := filepath.Join(t.TempDir(), ".memory")
	none := func(string) string { return "" }

	Due(home, none)
	Due(home, none) // the stamp is fresh now
	if started != 1 {
		t.Fatalf("started %d times", started)
	}
	stamp := filepath.Join(home, stampName)
	day := time.Now().Add(-25 * time.Hour)
	os.Chtimes(stamp, day, day)
	Due(home, func(k string) string {
		if k == "MEMORY_AUTO_UPDATE" {
			return "off"
		}
		return ""
	})
	if started != 1 {
		t.Fatal("started though switched off")
	}
	Version = "dev"
	Due(home, none)
	if started != 1 {
		t.Fatal("started for a dev build")
	}
	Version = "v0.2.0"
	Due(home, none)
	if started != 2 {
		t.Fatal("not started a day later")
	}
}
