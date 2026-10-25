package main

// Windows has no zone database for time.LoadLocation, and a -trimpath build
// has no GOROOT zoneinfo.zip to fall back to, so --timezone, -z and the
// timezone config resolve names from data embedded here, as upstream's jiff
// bundles tzdb on Windows.
import _ "time/tzdata"
