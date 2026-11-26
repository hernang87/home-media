package main

import (
	"io"
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

// fakeAgent is an agent over a temp home whose launchctl records its calls
// and fails the subcommands in fail.
func fakeAgent(t *testing.T, fail map[string]bool) (*agent, *[]string) {
	t.Helper()
	var calls []string
	a := &agent{
		home: t.TempDir(), exe: "/opt/mediad", workDir: t.TempDir(), domain: "gui/501",
		launchctl: func(args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			if fail[args[0]] {
				return []byte("Bootstrap failed: 5: Input/output error\n"), exec.ErrNotFound
			}
			return nil, nil
		},
	}
	return a, &calls
}

func TestAgentInstall(t *testing.T) {
	tests := []struct {
		name    string
		env     bool
		fail    map[string]bool
		wantErr string
		wantOut []string
	}{
		{"fresh", true, nil, "", []string{"ok   installed com.homemedia.mediad", "status: launchctl print gui/501/com.homemedia.mediad"}},
		{"no .env warns", false, nil, "", []string{"warn no .env in", "ok   installed"}},
		{"not loaded before", true, map[string]bool{"bootout": true}, "", []string{"ok   installed"}},
		{"bootstrap fails", true, map[string]bool{"bootstrap": true}, "launchctl bootstrap: executable file not found in $PATH: Bootstrap failed: 5: Input/output error", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, calls := fakeAgent(t, tt.fail)
			if tt.env {
				if err := os.WriteFile(filepath.Join(a.workDir, ".env"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out strings.Builder
			err := a.install(&out)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q:\n%s", want, out.String())
				}
			}

			plist := filepath.Join(a.home, "Library", "LaunchAgents", serviceLabel+".plist")
			b, err := os.ReadFile(plist)
			if err != nil {
				t.Fatalf("plist not written: %v", err)
			}
			logPath := filepath.Join(a.home, "Library", "Logs", "homemedia", "mediad.log")
			if !strings.Contains(string(b), "<string>"+logPath+"</string>") || !strings.Contains(string(b), "<string>/opt/mediad</string>") {
				t.Errorf("plist does not name exe and log path:\n%s", b)
			}
			wantCalls := []string{"bootout gui/501/" + serviceLabel, "bootstrap gui/501 " + plist}
			if strings.Join(*calls, "|") != strings.Join(wantCalls, "|") {
				t.Errorf("launchctl calls = %q, want %q", *calls, wantCalls)
			}
		})
	}
}

func TestAgentUninstall(t *testing.T) {
	for _, installed := range []bool{true, false} {
		a, calls := fakeAgent(t, map[string]bool{"bootout": !installed})
		plist := a.plistPath()
		if installed {
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(plist, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var out strings.Builder
		if err := a.uninstall(&out); err != nil {
			t.Fatalf("installed=%v: %v", installed, err)
		}
		want := "ok   no plist at "
		if installed {
			want = "ok   removed "
		}
		if !strings.Contains(out.String(), want+plist) || !strings.Contains(out.String(), "ok   unloaded "+serviceLabel) {
			t.Errorf("installed=%v: output:\n%s", installed, out.String())
		}
		if _, err := os.Stat(plist); !os.IsNotExist(err) {
			t.Errorf("installed=%v: plist still there", installed)
		}
		if len(*calls) != 1 || (*calls)[0] != "bootout gui/501/"+serviceLabel {
			t.Errorf("installed=%v: launchctl calls = %q", installed, *calls)
		}
	}
}

func TestAgentUninstallRemoveFails(t *testing.T) {
	a, _ := fakeAgent(t, nil)
	// A non-empty directory where the plist should be cannot be removed.
	if err := os.MkdirAll(filepath.Join(a.plistPath(), "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.uninstall(io.Discard); err == nil || !strings.Contains(err.Error(), "remove plist") {
		t.Fatalf("err = %v, want remove plist error", err)
	}
}
