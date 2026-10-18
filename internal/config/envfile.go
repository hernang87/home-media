package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EnvFile is the settings file Load reads, relative to the working directory.
const EnvFile = ".env"

// UpdateEnvFile sets KEY=value lines in the env file at path and leaves
// everything else (comments, other keys, order) as it is. A key already in the
// file is replaced at its first occurrence, the one Load honours; a new key is
// appended. An empty value writes "KEY=" rather than removing the line. The
// file is replaced atomically and ends up with mode 0600, since it can hold a
// secret. A missing file is created.
func UpdateEnvFile(path string, updates map[string]string) error {
	for k, v := range updates {
		if !envLineRE.MatchString(k + "=") {
			return fmt.Errorf("invalid setting name %q", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("%s: value must be a single line", k)
		}
	}

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		if text == "" {
			lines = nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	done := map[string]bool{}
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		m := envLineRE.FindStringSubmatch(line)
		if m == nil || done[m[1]] {
			continue
		}
		if v, ok := updates[m[1]]; ok {
			lines[i] = m[1] + "=" + v
			done[m[1]] = true
		} else {
			done[m[1]] = true // later duplicates of other keys stay untouched
		}
	}
	for _, k := range sortedKeys(updates) {
		if !wasSeen(lines, k) {
			lines = append(lines, k+"="+updates[k])
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".env-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// wasSeen reports whether lines hold an assignment to key.
func wasSeen(lines []string, key string) bool {
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if m := envLineRE.FindStringSubmatch(line); m != nil && m[1] == key {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
