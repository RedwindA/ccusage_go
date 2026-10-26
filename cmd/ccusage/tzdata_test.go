package main

import (
	"go/build"
	"slices"
	"testing"
)

// Windows has no zone database of its own, and a -trimpath release build has
// no GOROOT to fall back to, so a Windows binary needs embedded time zone
// data to resolve --timezone America/New_York, as upstream bundles it there.
func TestWindowsBuildEmbedsTimeZoneData(t *testing.T) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = "windows", "amd64", false
	pkg, err := ctx.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pkg.Imports, "time/tzdata") {
		t.Fatal("the Windows build does not embed time/tzdata")
	}
}
