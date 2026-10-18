package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	t.Setenv("HOMEMEDIA_MEDIAD", "")
	t.Setenv("HOMEMEDIA_DIR", "")
	t.Setenv("HOME", t.TempDir())

	t.Run("app bundle", func(t *testing.T) {
		root := t.TempDir()
		macos := filepath.Join(root, "App.app", "Contents", "MacOS")
		res := filepath.Join(root, "App.app", "Contents", "Resources")
		for _, d := range []string{macos, res} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeScript(t, filepath.Join(macos, "mediad"), "")
		if err := os.WriteFile(filepath.Join(res, "workdir"), []byte("/some/repo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Find(filepath.Join(macos, "homemedia-app"))
		if err != nil {
			t.Fatal(err)
		}
		if c.Dir != "/some/repo" || c.Binary != filepath.Join(macos, "mediad") {
			t.Errorf("got %+v", c)
		}
	})

	t.Run("repo bin dir", func(t *testing.T) {
		repo := t.TempDir()
		bin := filepath.Join(repo, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		writeScript(t, filepath.Join(bin, "mediad"), "")
		if err := os.WriteFile(filepath.Join(repo, ".env"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Find(filepath.Join(bin, "homemedia-app"))
		if err != nil {
			t.Fatal(err)
		}
		if c.Dir != repo {
			t.Errorf("Dir = %q, want %q", c.Dir, repo)
		}
	})

	t.Run("errors", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Find(filepath.Join(dir, "homemedia-app")); err == nil || !strings.Contains(err.Error(), "mediad not found") {
			t.Errorf("missing binary: err = %v", err)
		}
		writeScript(t, filepath.Join(dir, "mediad"), "")
		if _, err := Find(filepath.Join(dir, "homemedia-app")); err == nil || !strings.Contains(err.Error(), "working directory") {
			t.Errorf("missing dir: err = %v", err)
		}
	})
}

// alive reports whether pid is still a running process.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestStopTerminatesChild(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mediad")
	pidFile := filepath.Join(dir, "env")
	writeScript(t, bin, `echo "$`+ParentEnv+` $PORT" > env; exec sleep 60`)
	p, err := Start(Config{Binary: bin, Dir: dir, LogPath: filepath.Join(dir, "log", "mediad.log"), Port: 8123})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
			want := strconv.Itoa(os.Getpid()) + " 8123"
			if got := strings.TrimSpace(string(b)); got != want {
				t.Fatalf("child env = %q, want %q", got, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never started")
		}
		time.Sleep(20 * time.Millisecond)
	}

	p.Stop()
	p.Stop() // idempotent
	select {
	case <-p.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
	if alive(pid) {
		t.Errorf("child %d still running after Stop", pid)
	}
}

func TestStopKillsChildThatIgnoresSIGTERM(t *testing.T) {
	old := stopTimeout
	stopTimeout = 300 * time.Millisecond
	defer func() { stopTimeout = old }()

	dir := t.TempDir()
	bin := filepath.Join(dir, "mediad")
	writeScript(t, bin, "trap '' TERM\nwhile :; do sleep 1; done")
	p, err := Start(Config{Binary: bin, Dir: dir, LogPath: filepath.Join(dir, "m.log"), Port: 8124})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let the shell install its trap
	start := time.Now()
	p.Stop()
	if time.Since(start) < 250*time.Millisecond {
		t.Error("Stop returned before the SIGTERM grace period; the trap was not in place")
	}
	if alive(p.cmd.Process.Pid) {
		t.Error("child survived Stop")
	}
}
