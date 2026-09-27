package loader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugParseErrorUsesEncodingJSONText(t *testing.T) {
	for _, line := range []string{`{"type":`, `{"a":1}x`, `{"a":tru}`, `[1]`, `{"a":1e999}`} {
		path := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		l := New()
		l.SetDebug(true)
		_, _, msg, err := l.parseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]interface{}
		want := fmt.Sprintf("Line 1: JSON parse error: %v", json.Unmarshal([]byte(line), &raw))
		if !strings.Contains(msg, want) {
			t.Fatalf("%s: debug message %q does not contain %q", line, msg, want)
		}
	}
}
