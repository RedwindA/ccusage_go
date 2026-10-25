// Package selfupdate replaces the running binary with a GitHub release,
// verified the same way install.sh verifies it: the archive must match its
// SHA-256 line in the release's checksums.txt.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// RepoURL is the GitHub repository releases are published to.
const RepoURL = "https://github.com/RedwindA/ccusage_go"

// maxDownload bounds a release archive; current ones are about 10MB.
const maxDownload = 256 << 20

var tagPattern = regexp.MustCompile(`^v[0-9][0-9A-Za-z._-]*$`)

// ValidTag reports whether tag looks like a release tag such as v0.18.0.
func ValidTag(tag string) bool { return tagPattern.MatchString(tag) }

// Updater downloads releases for one platform.
type Updater struct {
	Client       *http.Client
	RepoURL      string
	GOOS, GOARCH string
}

// New returns an Updater for this binary's platform.
func New() *Updater {
	return &Updater{
		Client:  &http.Client{Timeout: 5 * time.Minute},
		RepoURL: RepoURL,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}
}

// Latest returns the tag of the latest stable release. It reads the
// redirect of /releases/latest, as install.sh does, which avoids the API's
// unauthenticated rate limit.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.RepoURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	client := *u.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("check latest release: %w", err)
	}
	resp.Body.Close()
	location, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("check latest release: unexpected HTTP %d", resp.StatusCode)
	}
	prefix := u.RepoURL + "/releases/tag/"
	tag := strings.TrimPrefix(location.String(), prefix)
	if !strings.HasPrefix(location.String(), prefix) || !ValidTag(tag) {
		return "", fmt.Errorf("check latest release: unexpected redirect to %s", location)
	}
	return tag, nil
}

// assetName is the release archive's binary name for the platform.
func (u *Updater) assetName() string {
	name := "ccusage_go-" + u.GOOS + "-" + u.GOARCH
	if u.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func (u *Updater) archiveName() string {
	if u.GOOS == "windows" {
		return u.assetName() + ".zip"
	}
	return u.assetName() + ".tar.gz"
}

// Download fetches the release archive for tag, verifies its checksum and
// returns the binary it contains.
func (u *Updater) Download(ctx context.Context, tag string) ([]byte, error) {
	if !ValidTag(tag) {
		return nil, fmt.Errorf("invalid release tag %q", tag)
	}
	base := u.RepoURL + "/releases/download/" + tag + "/"
	archive := u.archiveName()
	sums, err := u.get(ctx, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	want, err := checksumFor(sums, archive)
	if err != nil {
		return nil, err
	}
	data, err := u.get(ctx, base+archive)
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s; update aborted", archive)
	}
	var binary []byte
	if u.GOOS == "windows" {
		binary, err = fromZip(data, u.assetName())
	} else {
		binary, err = fromTarGz(data, u.assetName())
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", archive, err)
	}
	if len(binary) == 0 {
		return nil, fmt.Errorf("%s contains an empty binary", archive)
	}
	return binary, nil
}

func (u *Updater) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("download %s: larger than %d bytes", url, maxDownload)
	}
	return data, nil
}

// checksumFor returns the SHA-256 listed for name in a sha256sum file.
func checksumFor(sums []byte, name string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			sum := strings.ToLower(fields[0])
			if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
				break
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("missing or invalid checksum for %s", name)
}

func fromTarGz(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

func fromZip(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, maxDownload))
	}
	return nil, fmt.Errorf("%s not found", name)
}

// Executable returns the real path of the running binary.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Install replaces the binary at exe with binary once a staged copy next to
// it reports wantVersion from --version. The replacement is a rename, so exe
// is never left partially written.
func Install(ctx context.Context, exe string, binary []byte, wantVersion string) error {
	dir := filepath.Dir(exe)
	pattern := ".ccusage_go-update-*"
	if runtime.GOOS == "windows" {
		RemoveStale(exe)
		pattern += ".exe" // Windows only runs files with an executable extension
	}
	staged, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w (rerun with write access there, or reinstall into a user directory)", dir, err)
	}
	tmp := staged.Name()
	defer os.Remove(tmp) // no-op once renamed into place
	_, err = staged.Write(binary)
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o755)
	}
	if err != nil {
		return fmt.Errorf("stage update: %w", err)
	}
	if err := checkVersion(ctx, tmp, wantVersion); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return replaceAside(exe, tmp)
	}
	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

// replaceAside moves staged to exe on Windows, where a running executable
// cannot be replaced but can be renamed. The old binary moves to exe.old,
// which RemoveStale deletes once nothing runs it; while an earlier exe.old is
// still running (say, a blocks --live session), it gets a unique name.
func replaceAside(exe, staged string) error {
	aside := exe + ".old"
	if _, err := os.Lstat(aside); err == nil {
		aside = fmt.Sprintf("%s.old-%d", exe, time.Now().UnixNano())
	}
	if err := rename(exe, aside); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	if err := rename(staged, exe); err != nil {
		_ = rename(aside, exe)
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

// rename retries for a moment on Windows, where antivirus scanners briefly
// lock a freshly written executable and the rename fails with access denied.
func rename(from, to string) error {
	err := os.Rename(from, to)
	for i := 0; i < 20 && err != nil && runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission); i++ {
		time.Sleep(50 * time.Millisecond)
		err = os.Rename(from, to)
	}
	return err
}

// RemoveStale deletes the copies of exe that a Windows update or install.ps1
// renamed aside. Each is deleted once no process runs it any more; the rest
// stay for a later call.
func RemoveStale(exe string) {
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		return
	}
	prefix := filepath.Base(exe) + ".old"
	for _, e := range entries {
		name := e.Name()
		if (name == prefix || strings.HasPrefix(name, prefix+"-")) && !e.IsDir() {
			_ = os.Remove(filepath.Join(filepath.Dir(exe), name))
		}
	}
}

// checkVersion runs the staged binary, which must print wantVersion.
func checkVersion(ctx context.Context, path, wantVersion string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return fmt.Errorf("downloaded binary does not run: %w", err)
	}
	if !strings.Contains(string(out), wantVersion) {
		return errors.New("downloaded binary reports " + strings.TrimSpace(string(out)) + ", expected " + wantVersion)
	}
	return nil
}
