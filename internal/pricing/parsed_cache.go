package pricing

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
)

// Parsing the online price lists costs far more than revalidating them, and
// they rarely change. Next to each HTTP cache body we keep the parsed table,
// keyed by the body's hash and the identity of the code that parsed it, so an
// unchanged list (HTTP 304) is decoded from a compact binary file instead.
// Tables derived from the embedded snapshots are cached the same way, keyed
// by the build alone.

// parsedCacheFormat versions the encoding below.
const parsedCacheFormat = "ccusage-go parsed prices v1"

// buildIdentity names the code that parses price lists, or "" when the build
// cannot be identified (uncommitted changes, no VCS or module version), which
// disables the cache: parsing logic and embedded rules may differ from
// whatever wrote an existing file.
var buildIdentity = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	revision, modified := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	id := revision
	if revision == "" {
		id = info.Main.Version // go install module@version
		if id == "" || id == "(devel)" {
			return ""
		}
	} else if modified {
		return ""
	}
	return id + " " + runtime.Version()
})

// cachedParse returns parse(data), reusing the parsed-table cache for url
// when this build parsed the same body before.
func cachedParse(url string, data []byte, parse func([]byte) map[string]ModelPricing) map[string]ModelPricing {
	path := pricingCachePath(url)
	if len(data) == 0 || path == "" {
		return parse(data)
	}
	sum := sha256.Sum256(data)
	return cachedPrices(path+".parsed", url+"\n"+hex.EncodeToString(sum[:]), func() map[string]ModelPricing { return parse(data) })
}

// cachedEmbedded returns compute(), a table derived only from data embedded
// in the binary, reusing the copy this build cached under name.
func cachedEmbedded(name string, compute func() map[string]ModelPricing) map[string]ModelPricing {
	dir := pricingCacheDir()
	if dir == "" {
		return compute()
	}
	return cachedPrices(filepath.Join(dir, "embedded-"+name+".parsed"), "embedded "+name, compute)
}

// cachedPrices returns the table cached at path for this build and input,
// computing and storing it on a miss. Empty tables are not cached.
func cachedPrices(path, input string, compute func() map[string]ModelPricing) map[string]ModelPricing {
	identity := buildIdentity()
	if identity == "" {
		return compute()
	}
	key := parsedCacheFormat + "\n" + identity + "\n" + input + "\n"
	if prices, err := readParsedCache(path, key); err == nil {
		return prices
	}
	prices := compute()
	if len(prices) > 0 {
		writeParsedCache(path, key, prices)
	}
	return prices
}

func readParsedCache(path, key string) (map[string]ModelPricing, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(raw, []byte(key)) || len(raw) < len(key)+4 {
		return nil, errParsedCache
	}
	body, sum := raw[len(key):len(raw)-4], raw[len(raw)-4:]
	if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(sum) {
		return nil, errParsedCache
	}
	return decodePrices(body)
}

func writeParsedCache(path, key string, prices map[string]ModelPricing) {
	body := encodePrices(prices)
	out := make([]byte, 0, len(key)+len(body)+4)
	out = append(out, key...)
	out = append(out, body...)
	out = binary.LittleEndian.AppendUint32(out, crc32.ChecksumIEEE(body))
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), "parsed-*")
	if err != nil {
		return
	}
	tmp := file.Name()
	_, err = file.Write(out)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}

var errParsedCache = errors.New("invalid parsed price cache")

// Entry flags. Every ModelPricing field is encoded; TestPriceEncodingCoversFields
// fails when the struct gains a field the encoding does not know about.
const (
	flagCacheCreationAsInput = 1 << iota
	flagExactOnly
	flagCacheReadExplicit
	flagCacheCreateExplicit
	flagInputAbove
	flagOutputAbove
	flagCacheCreateAbove
	flagCacheReadAbove
)

func encodePrices(prices map[string]ModelPricing) []byte {
	out := binary.AppendUvarint(nil, uint64(len(prices)))
	putFloat := func(v float64) { out = binary.LittleEndian.AppendUint64(out, math.Float64bits(v)) }
	for name, p := range prices {
		out = binary.AppendUvarint(out, uint64(len(name)))
		out = append(out, name...)
		flags := byte(0)
		set := func(ok bool, flag byte) {
			if ok {
				flags |= flag
			}
		}
		set(p.CacheCreationAsInput, flagCacheCreationAsInput)
		set(p.ExactOnly, flagExactOnly)
		set(p.cacheReadExplicit, flagCacheReadExplicit)
		set(p.cacheCreateExplicit, flagCacheCreateExplicit)
		set(p.InputAbove != nil, flagInputAbove)
		set(p.OutputAbove != nil, flagOutputAbove)
		set(p.CacheCreateAbove != nil, flagCacheCreateAbove)
		set(p.CacheReadAbove != nil, flagCacheReadAbove)
		out = append(out, flags)
		for _, v := range []float64{p.InputCostPerToken, p.OutputCostPerToken, p.CacheCreationInputTokenCost, p.CacheReadInputTokenCost, p.FastMultiplier, p.WebSearchCostPerRequest, p.WebFetchCostPerRequest} {
			putFloat(v)
		}
		for _, v := range []*float64{p.InputAbove, p.OutputAbove, p.CacheCreateAbove, p.CacheReadAbove} {
			if v != nil {
				putFloat(*v)
			}
		}
		out = binary.AppendVarint(out, int64(p.LongContextThreshold))
		out = binary.AppendVarint(out, int64(p.MaxInputTokens))
	}
	return out
}

func decodePrices(b []byte) (map[string]ModelPricing, error) {
	d := priceDecoder{b: b}
	count := d.uvarint()
	if d.err != nil || count > uint64(len(b)) {
		return nil, errParsedCache
	}
	prices := make(map[string]ModelPricing, count)
	for i := uint64(0); i < count && d.err == nil; i++ {
		name := string(d.bytes(d.uvarint()))
		flags := d.byte()
		p := ModelPricing{
			CacheCreationAsInput: flags&flagCacheCreationAsInput != 0,
			ExactOnly:            flags&flagExactOnly != 0,
			cacheReadExplicit:    flags&flagCacheReadExplicit != 0,
			cacheCreateExplicit:  flags&flagCacheCreateExplicit != 0,
		}
		p.InputCostPerToken = d.float()
		p.OutputCostPerToken = d.float()
		p.CacheCreationInputTokenCost = d.float()
		p.CacheReadInputTokenCost = d.float()
		p.FastMultiplier = d.float()
		p.WebSearchCostPerRequest = d.float()
		p.WebFetchCostPerRequest = d.float()
		p.InputAbove = d.optionalFloat(flags&flagInputAbove != 0)
		p.OutputAbove = d.optionalFloat(flags&flagOutputAbove != 0)
		p.CacheCreateAbove = d.optionalFloat(flags&flagCacheCreateAbove != 0)
		p.CacheReadAbove = d.optionalFloat(flags&flagCacheReadAbove != 0)
		p.LongContextThreshold = d.int()
		p.MaxInputTokens = d.int()
		prices[name] = p
	}
	if d.err != nil || len(d.b) != 0 || uint64(len(prices)) != count {
		return nil, errParsedCache
	}
	return prices, nil
}

type priceDecoder struct {
	b   []byte
	err error
}

func (d *priceDecoder) bytes(n uint64) []byte {
	if d.err != nil || n > uint64(len(d.b)) {
		d.err = errParsedCache
		return nil
	}
	out := d.b[:n]
	d.b = d.b[n:]
	return out
}

func (d *priceDecoder) byte() byte {
	if b := d.bytes(1); b != nil {
		return b[0]
	}
	return 0
}

func (d *priceDecoder) float() float64 {
	if b := d.bytes(8); b != nil {
		return math.Float64frombits(binary.LittleEndian.Uint64(b))
	}
	return 0
}

func (d *priceDecoder) optionalFloat(present bool) *float64 {
	if !present {
		return nil
	}
	v := d.float()
	return &v
}

func (d *priceDecoder) uvarint() uint64 {
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.err = errParsedCache
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *priceDecoder) int() int {
	v, n := binary.Varint(d.b)
	if n <= 0 || v < math.MinInt || v > math.MaxInt {
		d.err = errParsedCache
		return 0
	}
	d.b = d.b[n:]
	return int(v)
}
