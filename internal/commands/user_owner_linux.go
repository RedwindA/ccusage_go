package commands

import (
	"os"
	"strconv"
	"syscall"
)

func homeOwnerMatches(info os.FileInfo, uid string) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return strconv.FormatUint(uint64(stat.Uid), 10) == uid
}
