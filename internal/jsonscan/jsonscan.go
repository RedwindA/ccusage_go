// Package jsonscan decodes JSON without reflection. Its results match
// encoding/json unmarshalling into interface{} and map[string]interface{}
// (same values, same accept/reject decisions), which the fuzz tests check
// against the standard library, but it can also walk an object's fields
// while skipping, without materializing, the values a caller does not need.
package jsonscan

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// maxDepth mirrors encoding/json's nesting limit.
const maxDepth = 10000

// SyntaxError reports malformed JSON.
type SyntaxError struct {
	Offset int
	msg    string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("%s at offset %d", e.msg, e.Offset) }

// Fields walks the JSON object in data in one pass, calling visit with each
// unescaped key and its Value in document order. visit may consume the value
// (Decode, Fields, Raw); values it leaves alone are skipped. Either way the
// whole input is validated as json.Unmarshal into
// map[string]json.RawMessage would (syntax only). A top-level null visits
// nothing and reports null; any other non-object is an error. Keys may
// repeat; callers that store them get encoding/json's last-one-wins behavior
// by assigning in visit order.
func Fields(data []byte, visit func(key []byte, v Value) error) (null bool, err error) {
	return fields(&scanner{b: data}, visit)
}

// FieldsStrict is Fields for callers standing in for json.Unmarshal into
// map[string]interface{}, which also rejects numbers outside float64 range
// anywhere in the input, including skipped values.
func FieldsStrict(data []byte, visit func(key []byte, v Value) error) (null bool, err error) {
	return fields(&scanner{b: data, strict: true}, visit)
}

func fields(s *scanner, visit func(key []byte, v Value) error) (null bool, err error) {
	s.ws()
	if null, err = s.fields(visit); err != nil {
		return false, err
	}
	return null, s.end()
}

// Value is an object member's value as seen by a Fields callback, which may
// consume it at most once. It is only valid during the callback.
type Value struct{ s *scanner }

// Kind returns the value's first byte: '{', '[', '"', 't', 'f', 'n', '-' or
// a digit (0 if the input ends early).
func (v Value) Kind() byte {
	if v.s.i >= len(v.s.b) {
		return 0
	}
	return v.s.b[v.s.i]
}

// Raw consumes the value and returns its bytes, which alias the input.
func (v Value) Raw() ([]byte, error) {
	start := v.s.i
	err := v.s.skip()
	return v.s.b[start:v.s.i], err
}

// Decode consumes the value, decoding it as json.Unmarshal into interface{}.
func (v Value) Decode() (interface{}, error) { return v.s.decode() }

// Fields consumes an object (or null) value, walking its members like the
// package-level Fields.
func (v Value) Fields(visit func(key []byte, v Value) error) (null bool, err error) {
	return v.s.fields(visit)
}

func (s *scanner) fields(visit func(key []byte, v Value) error) (null bool, err error) {
	if s.i < len(s.b) && s.b[s.i] == 'n' {
		return true, s.literal("null")
	}
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return false, s.fail("expected object")
	}
	return false, s.object(visit)
}

// Decode decodes one JSON value as json.Unmarshal into interface{} would.
func Decode(data []byte) (interface{}, error) {
	s := scanner{b: data, strict: true}
	s.ws()
	v, err := s.decode()
	if err != nil {
		return nil, err
	}
	return v, s.end()
}

// DecodeObject decodes data as json.Unmarshal into map[string]interface{}
// would: a top-level null yields a nil map, other non-objects are errors.
func DecodeObject(data []byte) (map[string]interface{}, error) {
	s := scanner{b: data, strict: true}
	s.ws()
	if s.i < len(s.b) && s.b[s.i] == 'n' {
		if err := s.literal("null"); err != nil {
			return nil, err
		}
		return nil, s.end()
	}
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return nil, s.fail("expected object")
	}
	m, err := s.decodeObject()
	if err != nil {
		return nil, err
	}
	return m, s.end()
}

type scanner struct {
	b     []byte
	i     int
	depth int
	// strict makes skip reject numbers outside float64 range, as decoding
	// into interface{} does; decode always parses, so it always checks.
	strict bool
}

func (s *scanner) fail(msg string) error { return &SyntaxError{Offset: s.i, msg: msg} }

func (s *scanner) end() error {
	s.ws()
	if s.i != len(s.b) {
		return s.fail("unexpected data after top-level value")
	}
	return nil
}

func (s *scanner) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

func (s *scanner) push() error {
	s.depth++
	if s.depth > maxDepth {
		return s.fail("exceeded max depth")
	}
	return nil
}

// object scans the object at s.i ('{'), calling visit (when non-nil) with
// each unescaped key and its value, then skipping the value unless visit
// consumed it.
func (s *scanner) object(visit func(key []byte, v Value) error) error {
	if err := s.push(); err != nil {
		return err
	}
	s.i++
	s.ws()
	if s.i < len(s.b) && s.b[s.i] == '}' {
		s.i++
		s.depth--
		return nil
	}
	for {
		key, err := s.key()
		if err != nil {
			return err
		}
		start := s.i
		if visit != nil {
			if err := visit(key, Value{s}); err != nil {
				return err
			}
		}
		if s.i == start { // not consumed
			if err := s.skip(); err != nil {
				return err
			}
		}
		if done, err := s.next('}'); done || err != nil {
			return err
		}
	}
}

// key scans `"key" :` and leaves s.i at the value.
func (s *scanner) key() ([]byte, error) {
	if s.i >= len(s.b) || s.b[s.i] != '"' {
		return nil, s.fail("expected object key")
	}
	start := s.i
	escaped, err := s.str()
	if err != nil {
		return nil, err
	}
	key := s.b[start+1 : s.i-1]
	if escaped || !utf8.Valid(key) {
		key = unquote(key)
	}
	s.ws()
	if s.i >= len(s.b) || s.b[s.i] != ':' {
		return nil, s.fail("expected ':'")
	}
	s.i++
	s.ws()
	return key, nil
}

// next consumes the separator after a member or element, reporting whether
// the closing delimiter ended the container.
func (s *scanner) next(closer byte) (bool, error) {
	s.ws()
	if s.i >= len(s.b) {
		return false, s.fail("unexpected end of input")
	}
	switch s.b[s.i] {
	case ',':
		s.i++
		s.ws()
		return false, nil
	case closer:
		s.i++
		s.depth--
		return true, nil
	}
	return false, s.fail("expected ',' or closing delimiter")
}

// skip validates and skips one value.
func (s *scanner) skip() error {
	if s.i >= len(s.b) {
		return s.fail("unexpected end of input")
	}
	switch c := s.b[s.i]; {
	case c == '{':
		return s.object(nil)
	case c == '[':
		if err := s.push(); err != nil {
			return err
		}
		s.i++
		s.ws()
		if s.i < len(s.b) && s.b[s.i] == ']' {
			s.i++
			s.depth--
			return nil
		}
		for {
			if err := s.skip(); err != nil {
				return err
			}
			if done, err := s.next(']'); done || err != nil {
				return err
			}
		}
	case c == '"':
		_, err := s.str()
		return err
	case c == 't':
		return s.literal("true")
	case c == 'f':
		return s.literal("false")
	case c == 'n':
		return s.literal("null")
	case c == '-' || (c >= '0' && c <= '9'):
		_, err := s.number(s.strict)
		return err
	}
	return s.fail("invalid character")
}

// decode decodes one value.
func (s *scanner) decode() (interface{}, error) {
	if s.i >= len(s.b) {
		return nil, s.fail("unexpected end of input")
	}
	switch c := s.b[s.i]; {
	case c == '{':
		return s.decodeObject()
	case c == '[':
		if err := s.push(); err != nil {
			return nil, err
		}
		s.i++
		s.ws()
		out := []interface{}{}
		if s.i < len(s.b) && s.b[s.i] == ']' {
			s.i++
			s.depth--
			return out, nil
		}
		for {
			v, err := s.decode()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			if done, err := s.next(']'); err != nil {
				return nil, err
			} else if done {
				return out, nil
			}
		}
	case c == '"':
		start := s.i
		escaped, err := s.str()
		if err != nil {
			return nil, err
		}
		body := s.b[start+1 : s.i-1]
		if escaped || !utf8.Valid(body) {
			return string(unquote(body)), nil
		}
		return string(body), nil
	case c == 't':
		return true, s.literal("true")
	case c == 'f':
		return false, s.literal("false")
	case c == 'n':
		return nil, s.literal("null")
	case c == '-' || (c >= '0' && c <= '9'):
		return s.number(true)
	}
	return nil, s.fail("invalid character")
}

func (s *scanner) decodeObject() (map[string]interface{}, error) {
	if err := s.push(); err != nil {
		return nil, err
	}
	s.i++
	s.ws()
	m := map[string]interface{}{}
	if s.i < len(s.b) && s.b[s.i] == '}' {
		s.i++
		s.depth--
		return m, nil
	}
	for {
		key, err := s.key()
		if err != nil {
			return nil, err
		}
		v, err := s.decode()
		if err != nil {
			return nil, err
		}
		m[string(key)] = v
		if done, err := s.next('}'); err != nil {
			return nil, err
		} else if done {
			return m, nil
		}
	}
}

// stringPlain marks bytes that can appear unescaped inside a JSON string
// without ending it.
var stringPlain = func() (t [256]bool) {
	for c := 0x20; c < 256; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

// str skips the string at s.i and reports whether it contains escapes.
func (s *scanner) str() (escaped bool, err error) {
	b := s.b
	i := s.i + 1
	for {
		// Skip eight plain bytes at a time; the byte loop finds the stop.
		for i+8 <= len(b) && !stopsString(binary.LittleEndian.Uint64(b[i:])) {
			i += 8
		}
		for i < len(b) && stringPlain[b[i]] {
			i++
		}
		if i >= len(b) {
			s.i = i
			return escaped, s.fail("unterminated string")
		}
		switch b[i] {
		case '"':
			s.i = i + 1
			return escaped, nil
		case '\\':
			escaped = true
			if i+1 >= len(b) {
				s.i = i
				return escaped, s.fail("unterminated string")
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(b) || !isHex(b[i+2]) || !isHex(b[i+3]) || !isHex(b[i+4]) || !isHex(b[i+5]) {
					s.i = i
					return escaped, s.fail("invalid unicode escape")
				}
				i += 6
			default:
				s.i = i
				return escaped, s.fail("invalid escape")
			}
		default:
			s.i = i
			return escaped, s.fail("control character in string")
		}
	}
}

// stopsString reports whether any byte of the little-endian word w is '"',
// '\\' or a control character. The bit tests are exact for "any byte"
// (they may misattribute which byte), which is all the fast path needs.
func stopsString(w uint64) bool {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	quote := w ^ (ones * '"')
	backslash := w ^ (ones * '\\')
	return ((quote-ones)&^quote|(backslash-ones)&^backslash|(w-ones*0x20)&^w)&highs != 0
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (s *scanner) literal(word string) error {
	if len(s.b)-s.i < len(word) || string(s.b[s.i:s.i+len(word)]) != word {
		return s.fail("invalid literal")
	}
	s.i += len(word)
	return nil
}

// number scans a number matching
// -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?. With parse set it also
// converts it to float64, rejecting values outside float64 range as
// encoding/json does; otherwise it returns 0.
func (s *scanner) number(parse bool) (float64, error) {
	b := s.b
	start := s.i
	i := start
	if b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	default:
		s.i = i
		return 0, s.fail("invalid number")
	}
	simple := true
	if i < len(b) && b[i] == '.' {
		simple = false
		i++
		digits := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == digits {
			s.i = i
			return 0, s.fail("invalid number")
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		simple = false
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		digits := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == digits {
			s.i = i
			return 0, s.fail("invalid number")
		}
	}
	s.i = i
	if !parse {
		return 0, nil
	}
	// Token counts dominate; integers below 2^53 convert exactly.
	if simple && i-start <= 15 {
		var n int64 // 15 digits overflow a 32-bit int
		neg := b[start] == '-'
		for _, c := range b[start:i] {
			if c != '-' {
				n = n*10 + int64(c-'0')
			}
		}
		if neg {
			return -float64(n), nil
		}
		return float64(n), nil
	}
	f, err := strconv.ParseFloat(string(b[start:i]), 64)
	if err != nil {
		s.i = start
		return 0, s.fail("number out of range")
	}
	return f, nil
}

// unquote decodes the body of a syntactically valid JSON string exactly as
// encoding/json does: invalid UTF-8 and unpaired surrogates become U+FFFD.
func unquote(s []byte) []byte {
	out := make([]byte, 0, len(s))
	for r := 0; r < len(s); {
		c := s[r]
		switch {
		case c == '\\':
			switch s[r+1] {
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				rr := getu4(s[r:])
				r += 6
				if utf16.IsSurrogate(rr) {
					if rr1 := getu4(s[r:]); rr1 >= 0 {
						if dec := utf16.DecodeRune(rr, rr1); dec != unicode.ReplacementChar {
							r += 6
							out = utf8.AppendRune(out, dec)
							continue
						}
					}
					rr = unicode.ReplacementChar
				}
				out = utf8.AppendRune(out, rr)
				continue
			default: // '"', '\\', '/'
				out = append(out, s[r+1])
			}
			r += 2
		case c < utf8.RuneSelf:
			out = append(out, c)
			r++
		default:
			rr, size := utf8.DecodeRune(s[r:])
			out = utf8.AppendRune(out, rr)
			r += size
		}
	}
	return out
}

// getu4 decodes \uXXXX at the start of s, or returns -1.
func getu4(s []byte) rune {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return -1
	}
	var r rune
	for _, c := range s[2:6] {
		switch {
		case '0' <= c && c <= '9':
			c = c - '0'
		case 'a' <= c && c <= 'f':
			c = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			c = c - 'A' + 10
		default:
			return -1
		}
		r = r*16 + rune(c)
	}
	return r
}
