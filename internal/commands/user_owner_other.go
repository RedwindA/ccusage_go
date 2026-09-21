//go:build !linux

package commands

import "os"

func homeOwnerMatches(info os.FileInfo, uid string) bool { return false }
