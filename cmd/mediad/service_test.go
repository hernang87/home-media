package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlistForLints(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not found")
	}
	path := filepath.Join(t.TempDir(), serviceLabel+".plist")
	data := plistFor("/usr/local/bin/mediad", "/Users/me/dev/server", "/Users/me/Library/Logs/homemedia/mediad.log")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint: %v: %s", err, out)
	}
}

func TestPlistForContents(t *testing.T) {
	exe := "/Users/me/bin/mediad"
	workDir := "/Users/me/dev/server"
	logPath := "/Users/me/Library/Logs/homemedia/mediad.log"
	got := string(plistFor(exe, workDir, logPath))
	for _, want := range []string{serviceLabel, exe, workDir, logPath} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestPlistForEscapes(t *testing.T) {
	got := string(plistFor("/bin/mediad", "/Users/me/Tom & Jerry <dev>", "/tmp/log"))
	if !strings.Contains(got, "/Users/me/Tom &amp; Jerry &lt;dev&gt;") {
		t.Errorf("work dir not escaped in plist:\n%s", got)
	}
	if strings.Contains(got, "Tom & Jerry") {
		t.Errorf("raw ampersand present in plist")
	}
}
