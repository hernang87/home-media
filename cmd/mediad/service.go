package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const serviceLabel = "com.homemedia.mediad"

// plistTemplate is the launchd agent definition. Values are escaped by the
// esc function before insertion.
var plistTemplate = template.Must(template.New("plist").Funcs(template.FuncMap{
	"esc": xmlEscape,
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{esc .Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{esc .Exe}}</string>
	</array>
	<key>WorkingDirectory</key>
	<string>{{esc .WorkDir}}</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
	</dict>
	<key>StandardOutPath</key>
	<string>{{esc .LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{esc .LogPath}}</string>
</dict>
</plist>
`))

// xmlEscape escapes s for use as XML character data.
func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// plistFor renders the launchd plist for the daemon at exe, started in
// workDir, with stdout and stderr appended to logPath.
func plistFor(exe, workDir, logPath string) []byte {
	var b bytes.Buffer
	data := struct {
		Label, Exe, WorkDir, LogPath string
	}{serviceLabel, exe, workDir, logPath}
	if err := plistTemplate.Execute(&b, data); err != nil {
		// Only possible on a template bug; the template is static.
		panic(err)
	}
	return b.Bytes()
}

// agentDomain is the launchd domain of the current user's GUI session.
func agentDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// agent installs and removes the launchd agent. Tests swap its fields.
type agent struct {
	home, exe, workDir string
	domain             string                               // launchd domain, e.g. gui/501
	launchctl          func(args ...string) ([]byte, error) // runs launchctl, returning its combined output
}

// newAgent describes the agent for this executable, started in the working directory.
func newAgent() (*agent, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, fmt.Errorf("absolute executable path: %w", err)
	}
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home directory: %w", err)
	}
	return &agent{home: home, exe: exe, workDir: workDir, domain: agentDomain(), launchctl: func(args ...string) ([]byte, error) {
		return exec.Command("launchctl", args...).CombinedOutput()
	}}, nil
}

func (a *agent) target() string { return a.domain + "/" + serviceLabel }
func (a *agent) plistPath() string {
	return plistPathIn(filepath.Join(a.home, "Library", "LaunchAgents"))
}

// runInstall writes the launch agent plist and (re)loads it with launchctl.
// Run it from the repository root: mediad reads ./.env and writes ./data
// relative to its working directory.
func runInstall(w io.Writer) error {
	a, err := newAgent()
	if err != nil {
		return err
	}
	return a.install(w)
}

// runUninstall unloads the launch agent and removes its plist if present.
func runUninstall(w io.Writer) error {
	a, err := newAgent()
	if err != nil {
		return err
	}
	return a.uninstall(w)
}

func (a *agent) install(w io.Writer) error {
	if _, err := os.Stat(filepath.Join(a.workDir, ".env")); err != nil {
		fmt.Fprintf(w, "warn no .env in %s; mediad will rely on environment variables\n", a.workDir)
	}
	logDir := filepath.Join(a.home, "Library", "Logs", "homemedia")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, "mediad.log")

	plistPath := a.plistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(plistPath, plistFor(a.exe, a.workDir, logPath), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Unloading a service that is not loaded fails; that is expected.
	_, _ = a.launchctl("bootout", a.target())
	if out, err := a.launchctl("bootstrap", a.domain, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}

	fmt.Fprintf(w, "ok   installed %s\n", serviceLabel)
	fmt.Fprintf(w, "     executable: %s\n", a.exe)
	fmt.Fprintf(w, "     working dir: %s\n", a.workDir)
	fmt.Fprintf(w, "     plist: %s\n", plistPath)
	fmt.Fprintf(w, "     logs: tail -f %s\n", logPath)
	fmt.Fprintf(w, "     status: launchctl print %s\n", a.target())
	fmt.Fprintf(w, "     uninstall: mediad uninstall\n")
	return nil
}

func (a *agent) uninstall(w io.Writer) error {
	// Bootout fails when the agent is not loaded; that is not an error here.
	_, _ = a.launchctl("bootout", a.target())

	plistPath := a.plistPath()
	switch err := os.Remove(plistPath); {
	case err == nil:
		fmt.Fprintf(w, "ok   removed %s\n", plistPath)
	case os.IsNotExist(err):
		fmt.Fprintf(w, "ok   no plist at %s\n", plistPath)
	default:
		return fmt.Errorf("remove plist: %w", err)
	}
	fmt.Fprintf(w, "ok   unloaded %s\n", serviceLabel)
	return nil
}

func plistPathIn(dir string) string {
	return filepath.Join(dir, serviceLabel+".plist")
}
