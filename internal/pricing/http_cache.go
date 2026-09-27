package pricing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const pricingMaxBytes = 64 << 20

func pricingCacheDir() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(dir) {
		home, _ := os.UserHomeDir()
		if !filepath.IsAbs(home) {
			return ""
		}
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "ccusage-go", "http-cache")
}

func pricingCachePath(url string) string {
	dir := pricingCacheDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, fmtHash(url)+".cache")
}
func fmtHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	const hex = "0123456789abcdef"
	out := make([]byte, len(sum)*2)
	for i, b := range sum {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&15]
	}
	return string(out)
}
func validETag(tag string) bool {
	if tag == "" || len(tag) > 4096 {
		return false
	}
	for _, r := range tag {
		if r < 32 || r >= 127 {
			return false
		}
	}
	return true
}
func readPricingCache(path string) (string, []byte) {
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer file.Close()
	raw, err := readAllLimited(file, pricingMaxBytes+4098)
	if err != nil || len(raw) > pricingMaxBytes+4097 {
		return "", nil
	}
	etag, body, ok := bytes.Cut(raw, []byte{'\n'})
	tag := strings.TrimSpace(string(etag))
	if !ok || !validETag(tag) || len(body) > pricingMaxBytes {
		return "", nil
	}
	return tag, body
}

// readAllLimited is io.ReadAll(io.LimitReader(file, limit)) with the buffer
// sized from the file up front instead of grown by repeated copying.
func readAllLimited(file *os.File, limit int64) ([]byte, error) {
	size := int64(512)
	if info, err := file.Stat(); err == nil && info.Size() > 0 {
		size = min(info.Size()+1, limit) // +1 reaches EOF without growing
	}
	buf := make([]byte, 0, size)
	r := io.LimitReader(file, limit)
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
}

func writePricingCache(path, etag string, body []byte) {
	if path == "" || !validETag(etag) || len(body) > pricingMaxBytes {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), "pricing-*")
	if err != nil {
		return
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	_, err = file.Write(append(append([]byte(etag), '\n'), body...))
	closeErr := file.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(tmp, path)
	}
}
func (s *Service) fetch(ctx context.Context, url string) []byte {
	return fetchUsable(ctx, s.client, url, func(data []byte) bool { return validatesPricing(url, data, true) })
}

// fetchUsable is fetch with a caller-supplied check for a revalidated cache
// body, so callers that parse the body anyway can keep that parse instead of
// decoding the multi-megabyte payload twice.
func fetchUsable(ctx context.Context, client *http.Client, url string, usable func([]byte) bool) []byte {
	path := pricingCachePath(url)
	etag, cached := readPricingCache(path)
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil
		}
		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			if etag == "" {
				return nil
			}
			if usable(cached) {
				return cached
			}
			if path != "" {
				_ = os.Remove(path)
			}
			etag = ""
			cached = nil
			continue
		}
		if resp.StatusCode != http.StatusOK || resp.ContentLength > pricingMaxBytes {
			resp.Body.Close()
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, pricingMaxBytes+1))
		resp.Body.Close()
		if err != nil || len(data) > pricingMaxBytes || !validatesPricing(url, data, false) {
			return nil
		}
		writePricingCache(path, resp.Header.Get("ETag"), data)
		return data
	}
	return nil
}
func validatesPricing(url string, data []byte, usable bool) bool {
	if strings.Contains(url, "raw.githubusercontent.com") {
		return len(litePricing(data)) > 0
	}
	if usable {
		return len(parseModelsCatalog(data)) > 0
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || len(raw) == 0 {
		return false
	}
	providerShape := false
	for _, value := range raw {
		var v map[string]json.RawMessage
		if json.Unmarshal(value, &v) == nil && v["models"] != nil {
			providerShape = true
			break
		}
	}
	validCost := false
	costValid := func(value json.RawMessage) bool {
		var v struct {
			Cost struct {
				Input  *float64 `json:"input"`
				Output *float64 `json:"output"`
			} `json:"cost"`
		}
		if json.Unmarshal(value, &v) != nil {
			return false
		}
		return v.Cost.Input != nil && v.Cost.Output != nil && (*v.Cost.Input != 0 || *v.Cost.Output != 0)
	}
	for _, value := range raw {
		if providerShape {
			var provider struct {
				Models map[string]json.RawMessage `json:"models"`
			}
			if json.Unmarshal(value, &provider) != nil || provider.Models == nil {
				return false
			}
			for _, model := range provider.Models {
				var object map[string]any
				if json.Unmarshal(model, &object) != nil || object == nil {
					return false
				}
				validCost = validCost || costValid(model)
			}
		} else {
			var model map[string]json.RawMessage
			if json.Unmarshal(value, &model) != nil || model["cost"] == nil {
				return false
			}
			validCost = validCost || costValid(value)
		}
	}
	return validCost
}
