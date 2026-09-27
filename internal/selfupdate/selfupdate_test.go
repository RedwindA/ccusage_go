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
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestInstallReplacesAfterVersionCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the staged binary")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "ccusage_go")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := func(v string) []byte { return []byte("#!/bin/sh\necho ccusage_go version " + v + "\n") }

	if err := Install(context.Background(), exe, script("v1.0.0"), "v2.0.0"); err == nil {
		t.Fatal("installed a binary reporting the wrong version")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("failed install modified the binary: %q", b)
	}

	if err := Install(context.Background(), exe, script("v2.0.0"), "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, script("v2.0.0")) {
		t.Fatalf("binary not replaced: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("staging files left behind: %v", entries)
	}
}

func TestValidTag(t *testing.T) {
	for tag, want := range map[string]bool{"v0.18.0": true, "v1.0.0-rc.1": true, "0.18.0": false, "v": false, "v1/../x": false, "latest": false} {
		if ValidTag(tag) != want {
			t.Errorf("ValidTag(%q) = %v", tag, !want)
		}
	}
}
