// Package daemon lets the desktop app own a mediad child process: find the
// binary, start it, and stop it when the app quits.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// ParentEnv names the env var that tells mediad which process owns it. mediad
// shuts itself down when that process is gone, even if the owner was killed.
const ParentEnv = "HOMEMEDIA_PARENT_PID"

// stopTimeout is how long Stop waits after SIGTERM; mediad's own shutdown takes up to 5 s.
var stopTimeout = 8 * time.Second

// Config says how to start mediad.
type Config struct {
	Binary  string // path to mediad
	Dir     string // working dir: holds .env and data/
	LogPath string // stdout and stderr are appended here
	Port    int
}

// Find locates mediad and its working dir for an app executable at exe.
//
// Binary: $HOMEMEDIA_MEDIAD, else "mediad" next to exe.
// Dir: $HOMEMEDIA_DIR, else the path in ../Resources/workdir (written by
// scripts/package-app.sh), else exe's parent dir when it holds a .env (bin/ in the repo).
func Find(exe string) (Config, error) {
	exeDir := filepath.Dir(exe)
	bin := os.Getenv("HOMEMEDIA_MEDIAD")
	if bin == "" {
		bin = filepath.Join(exeDir, "mediad")
	}
	if fi, err := os.Stat(bin); err != nil || fi.IsDir() {
		return Config{}, fmt.Errorf("mediad not found at %s", bin)
	}

	dir := os.Getenv("HOMEMEDIA_DIR")
	if dir == "" {
		if b, err := os.ReadFile(filepath.Join(exeDir, "..", "Resources", "workdir")); err == nil {
			dir = string(trimNL(b))
		}
	}
	if dir == "" {
		if parent := filepath.Dir(exeDir); fileExists(filepath.Join(parent, ".env")) {
			dir = parent
		}
	}
	if dir == "" {
		return Config{}, errors.New("no working directory for mediad (set HOMEMEDIA_DIR)")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Binary:  bin,
		Dir:     dir,
		LogPath: filepath.Join(home, "Library", "Logs", "homemedia", "mediad.log"),
		Port:    8200,
	}, nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Proc is a running mediad child.
type Proc struct {
	cmd      *exec.Cmd
	log      *os.File
	done     chan struct{}
	stopOnce sync.Once
}

// Start launches mediad. The child gets its own process group and ParentEnv.
func Start(c Config) (*Proc, error) {
	if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o755); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(c.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(c.Binary)
	cmd.Dir = c.Dir
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A Finder-launched app has a bare PATH; mediad needs ffmpeg and ffprobe.
	// exec keeps the last value of a repeated key.
	cmd.Env = append(os.Environ(),
		"PATH=/opt/homebrew/bin:/usr/local/bin:"+os.Getenv("PATH"),
		"PORT="+strconv.Itoa(c.Port),
		ParentEnv+"="+strconv.Itoa(os.Getpid()),
	)
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return nil, err
	}
	p := &Proc{cmd: cmd, log: logf, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = logf.Close()
		close(p.done)
	}()
	return p, nil
}

// Done is closed when the child has exited.
func (p *Proc) Done() <-chan struct{} { return p.done }

// Stop asks mediad to shut down (SIGTERM), then kills its process group if it
// is still alive after stopTimeout. Safe to call more than once.
func (p *Proc) Stop() {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(stopTimeout):
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	})
}
