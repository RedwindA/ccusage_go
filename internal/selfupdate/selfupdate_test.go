package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

// fakeGitHub serves a release tag with the given assets under /releases.
func fakeGitHub(t *testing.T, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			http.Redirect(w, r, "http://"+r.Host+"/releases/tag/"+tag, http.StatusFound)
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, "/releases/download/"+tag+"/")
		if body, found := assets[name]; ok && found {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func checksums(assets map[string][]byte) []byte {
	var b strings.Builder
	for name, body := range assets {
		sum := sha256.Sum256(body)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return []byte(b.String())
}

func updater(srv *httptest.Server, goos string) *Updater {
	return &Updater{Client: srv.Client(), RepoURL: srv.URL, GOOS: goos, GOARCH: "amd64"}
}

func TestLatestReadsReleaseRedirect(t *testing.T) {
	srv := fakeGitHub(t, "v9.8.7", nil)
	got, err := updater(srv, "linux").Latest(context.Background())
	if err != nil || got != "v9.8.7" {
		t.Fatalf("Latest = %q, %v", got, err)
	}
	if _, err := updater(fakeGitHub(t, "not-a-tag", nil), "linux").Latest(context.Background()); err == nil {
		t.Fatal("accepted a redirect to an invalid tag")
	}
}

func TestDownloadVerifiesAndExtracts(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		u := &Updater{GOOS: goos, GOARCH: "amd64"}
		files := map[string]string{u.assetName(): "new binary", "LICENSE": "license"}
		archive := tarGz(t, files)
		if goos == "windows" {
			archive = zipOf(t, files)
		}
		assets := map[string][]byte{u.archiveName(): archive}
		assets["checksums.txt"] = checksums(assets)
		got, err := updater(fakeGitHub(t, "v1.2.3", assets), goos).Download(context.Background(), "v1.2.3")
		if err != nil || string(got) != "new binary" {
			t.Fatalf("%s: Download = %q, %v", goos, got, err)
		}
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	u := &Updater{GOOS: "linux", GOARCH: "amd64"}
	assets := map[string][]byte{u.archiveName(): tarGz(t, map[string]string{u.assetName(): "x"})}
	assets["checksums.txt"] = checksums(map[string][]byte{u.archiveName(): []byte("tampered")})
	_, err := updater(fakeGitHub(t, "v1.2.3", assets), "linux").Download(context.Background(), "v1.2.3")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	delete(assets, "checksums.txt")
	assets["checksums.txt"] = []byte("")
	if _, err := updater(fakeGitHub(t, "v1.2.3", assets), "linux").Download(context.Background(), "v1.2.3"); err == nil {
		t.Fatal("accepted an archive without a checksum")
	}
}

// TestMain lets tests use this test binary as the downloaded release: with
// FAKE_CCUSAGE_GO_VERSION set it prints that version, or with
// FAKE_CCUSAGE_GO_SLEEP it keeps running like a blocks --live session.
func TestMain(m *testing.M) {
	if v := os.Getenv("FAKE_CCUSAGE_GO_VERSION"); v != "" {
		fmt.Println("ccusage_go version " + v)
		os.Exit(0)
	}
	if os.Getenv("FAKE_CCUSAGE_GO_SLEEP") != "" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeRelease returns this test binary, which acts as a release reporting v.
func fakeRelease(t *testing.T, v string) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CCUSAGE_GO_VERSION", v)
	return binary
}

func installed(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestInstallReplacesAfterVersionCheck(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ccusage_go.exe")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Install(context.Background(), exe, fakeRelease(t, "v1.0.0"), "v2.0.0"); err == nil {
		t.Fatal("installed a binary reporting the wrong version")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("failed install modified the binary: %q", b)
	}
	if names := installed(t, dir); len(names) != 1 {
		t.Fatalf("failed install left files behind: %v", names)
	}

	binary := fakeRelease(t, "v2.0.0")
	if err := Install(context.Background(), exe, binary, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, binary) {
		t.Fatal("binary not replaced")
	}
	RemoveStale(exe)
	if names := installed(t, dir); len(names) != 1 {
		t.Fatalf("staging files left behind: %v", names)
	}
}

// Updating twice while the original binary keeps running must work: on
// Windows the second update cannot delete or reuse the first one's exe.old.
func TestInstallWhileOldBinaryRuns(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ccusage_go.exe")
	binary := fakeRelease(t, "v2.0.0")
	if err := os.WriteFile(exe, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	running := exec.Command(exe)
	running.Env = append(os.Environ(), "FAKE_CCUSAGE_GO_VERSION=", "FAKE_CCUSAGE_GO_SLEEP=1")
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { running.Process.Kill(); running.Wait() })

	for i := 0; i < 2; i++ {
		if err := Install(context.Background(), exe, binary, "v2.0.0"); err != nil {
			t.Fatalf("update %d: %v", i+1, err)
		}
	}
	running.Process.Kill()
	running.Wait()
	RemoveStale(exe)
	if names := installed(t, dir); len(names) != 1 || names[0] != "ccusage_go.exe" {
		t.Fatalf("files after updates: %v", names)
	}
}

func TestReplaceAsideAvoidsStaleCopyInUse(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ccusage_go.exe")
	staged := filepath.Join(dir, "staged.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	os.WriteFile(staged, []byte("new"), 0o755)
	// A directory stands in for an exe.old that cannot be deleted.
	if err := os.Mkdir(exe+".old", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceAside(exe, staged); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe = %q", b)
	}
	asides, _ := filepath.Glob(exe + ".old-*")
	if len(asides) != 1 {
		t.Fatalf("aside copies = %v", asides)
	}
	RemoveStale(exe)
	if names := installed(t, dir); len(names) != 2 {
		t.Fatalf("files after cleanup: %v", names)
	}
}

func TestValidTag(t *testing.T) {
	for tag, want := range map[string]bool{"v0.18.0": true, "v1.0.0-rc.1": true, "0.18.0": false, "v": false, "v1/../x": false, "latest": false} {
		if ValidTag(tag) != want {
			t.Errorf("ValidTag(%q) = %v", tag, !want)
		}
	}
}
