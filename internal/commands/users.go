package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type systemUser struct{ Name, Home string }

func systemUsers() ([]systemUser, error) {
	// getent includes NSS-backed accounts; /etc/passwd covers minimal systems.
	data, err := exec.Command("getent", "passwd").Output()
	if err != nil {
		data, err = os.ReadFile("/etc/passwd")
		if err != nil {
			return nil, err
		}
	}
	users := parseSystemUsers(string(data))
	if len(users) == 0 {
		return nil, fmt.Errorf("no system accounts have an existing home directory")
	}
	return users, nil
}

func parseSystemUsers(data string) []systemUser {
	byHome := map[string]systemUser{}
	lines := strings.Split(data, "\n")
	sort.Strings(lines)
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) < 7 || fields[0] == "" || !filepath.IsAbs(fields[5]) {
			continue
		}
		home, err := filepath.EvalSymlinks(fields[5])
		if err != nil {
			continue
		}
		info, err := os.Stat(home)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, ok := byHome[home]; !ok || homeOwnerMatches(info, fields[2]) {
			byHome[home] = systemUser{fields[0], home}
		}
	}
	users := make([]systemUser, 0, len(byHome))
	for _, u := range byHome {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users
}
