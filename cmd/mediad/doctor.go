package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"homemedia/internal/api"
	"homemedia/internal/cast"
	"homemedia/internal/config"
)

// doctorClient bounds every server probe so a dead port fails fast.
var doctorClient = &http.Client{Timeout: 2 * time.Second}

// runDoctor prints a checklist of the local setup and returns an error when
// any check failed outright (missing binaries or unusable config).
func runDoctor(w io.Writer) error {
	failed := false
	line := func(tag, format string, args ...any) {
		if tag == "FAIL" {
			failed = true
		}
		fmt.Fprintf(w, "%-4s %s\n", tag, fmt.Sprintf(format, args...))
	}

	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if err := checkBinary(bin); err != nil {
			line("FAIL", "%s not found in PATH (brew install ffmpeg)", bin)
		} else {
			line("ok", "%s found", bin)
		}
	}

	cfg, err := config.Load("")
	if err != nil {
		line("FAIL", "config: %v", err)
	} else {
		line("ok", "media dir: %s", cfg.MediaDir)
		line("ok", "port: %d", cfg.Port)
		if cfg.TMDBAPIKey == "" {
			line("warn", "no TMDB key: art uses video frames")
		} else {
			line("ok", "TMDB_API_KEY is set")
		}
	}

	lan := config.LANAddress()
	if lan == "127.0.0.1" {
		line("warn", "LAN address is 127.0.0.1; devices cannot reach this machine")
	} else {
		line("ok", "LAN address: %s", lan)
	}

	if cfg == nil {
		line("warn", "server check skipped: config did not load")
		return doctorResult(failed)
	}

	base := fmt.Sprintf("http://%s:%d", lan, cfg.Port)
	var info api.Info
	if err := getJSON(base+"/api/info", &info); err != nil {
		line("warn", "server not running on port %d", cfg.Port)
		return doctorResult(failed)
	}
	line("ok", "server is running as %s v%s", info.Name, info.Version)

	var devices []cast.Device
	if err := getJSON(base+"/api/cast/devices", &devices); err != nil {
		line("warn", "could not list Chromecasts: %v", err)
		return doctorResult(failed)
	}
	if len(devices) == 0 {
		line("warn", "no Chromecasts found; check Local Network permission and that the TV is on the same Wi-Fi")
	} else {
		names := make([]string, 0, len(devices))
		for _, d := range devices {
			names = append(names, d.Name)
		}
		line("ok", "Chromecasts: %s", strings.Join(names, ", "))
	}
	return doctorResult(failed)
}

func doctorResult(failed bool) error {
	if failed {
		return errors.New("doctor found failing checks")
	}
	return nil
}

// getJSON GETs url with the probe timeout and decodes a 200 response into v.
func getJSON(url string, v any) error {
	resp, err := doctorClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
