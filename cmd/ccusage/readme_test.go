package main

import (
	"os"
	"regexp"
	"testing"
)

// The READMEs name the Go version needed to build from source, which is the
// go directive in go.mod.
func TestReadmeGoVersionMatchesGoMod(t *testing.T) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	directive := regexp.MustCompile(`(?m)^go (\d+\.\d+)`).FindSubmatch(mod)
	if directive == nil {
		t.Fatal("go.mod has no go directive")
	}
	for _, name := range []string{"../../README.md", "../../README_ZH_TW.md"} {
		readme, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		mentions := regexp.MustCompile(`Go (\d+\.\d+)`).FindAllSubmatch(readme, -1)
		if len(mentions) == 0 {
			t.Fatalf("%s names no Go version", name)
		}
		for _, m := range mentions {
			if string(m[1]) != string(directive[1]) {
				t.Errorf("%s says Go %s, go.mod needs %s", name, m[1], directive[1])
			}
		}
	}
}
