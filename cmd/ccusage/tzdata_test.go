package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Windows has no zone database of its own, and a -trimpath release build has
// no GOROOT to fall back to, so a Windows binary needs embedded time zone
// data to resolve --timezone America/New_York, as upstream bundles it there.
func TestWindowsBuildEmbedsTimeZoneData(t *testing.T) {
	list := exec.Command("go", "list", "-deps", ".")
	list.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Fields(string(out)), "time/tzdata") {
		t.Fatal("the Windows build does not embed time/tzdata")
	}
}
