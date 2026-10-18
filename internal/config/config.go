// Package config loads settings from flags, environment and .env.
// Port of lib/config.js (node-ref).
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Config is the daemon configuration.
type Config struct {
	MediaDir     string // first existing of: arg, MEDIA_DIR, ~/Bajadas, ~/Documents/Bajadas
	Port         int    // PORT, default 8200
	ServerName   string // SERVER_NAME, default "Home Media"
	TMDBAPIKey   string // TMDB_API_KEY
	TMDBLanguage string // TMDB_LANGUAGE, default "en-US"
	DataDir      string // ./data next to the working dir; created
	ArtDir       string // DataDir/art; created
	TmpDir       string // os.TempDir()/home-media-hls
}

const (
	defaultPort       = 8200
	defaultServerName = "Home Media"
	defaultLanguage   = "en-US"
	dataDirName       = "data"
)

// envLineRE matches KEY=value lines the same way lib/config.js does.
var envLineRE = regexp.MustCompile(`^\s*([A-Z_][A-Z0-9_]*)\s*=\s*(.*?)\s*$`)

// Load reads an optional .env in the working directory (never overriding real
// env vars; # comments and quoted values supported), then env vars, with
// mediaArg (may be "") taking priority for MediaDir. Expands a leading "~".
// Returns an error when no media directory exists.
func Load(mediaArg string) (*Config, error) {
	dotEnv := parseDotEnv(".env")
	// Real environment variables win, even when set to an empty string.
	get := func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return dotEnv[key]
	}

	mediaDir, err := findMediaDir(mediaArg, get("MEDIA_DIR"))
	if err != nil {
		return nil, err
	}

	port := defaultPort
	if n, err := strconv.Atoi(strings.TrimSpace(get("PORT"))); err == nil && n > 0 && n <= 65535 {
		port = n
	}

	cfg := &Config{
		MediaDir:     mediaDir,
		Port:         port,
		ServerName:   orDefault(get("SERVER_NAME"), defaultServerName),
		TMDBAPIKey:   get("TMDB_API_KEY"),
		TMDBLanguage: orDefault(get("TMDB_LANGUAGE"), defaultLanguage),
		DataDir:      dataDirName,
		ArtDir:       filepath.Join(dataDirName, "art"),
		TmpDir:       filepath.Join(os.TempDir(), "home-media-hls"),
	}
	for _, dir := range []string{cfg.DataDir, cfg.ArtDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return cfg, nil
}

// LANAddress returns the IPv4 address devices on the LAN should use to reach
// this machine: prefers en0, skips loopback, utun*, docker*, bridge*, and
// link-local 169.254/16. Falls back to "127.0.0.1".
func LANAddress() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	first := ""
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if hasAnyPrefix(ifc.Name, "utun", "docker", "bridge", "vmnet", "veth", "awdl", "llw") {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			if ifc.Name == "en0" {
				return ip4.String()
			}
			if first == "" {
				first = ip4.String()
			}
		}
	}
	if first != "" {
		return first
	}
	return "127.0.0.1"
}

// findMediaDir returns the first candidate that exists and is a directory:
// mediaArg, MEDIA_DIR, ~/Bajadas, ~/Documents/Bajadas.
func findMediaDir(mediaArg, envMediaDir string) (string, error) {
	home, homeErr := os.UserHomeDir()
	type candidate struct{ label, path string }
	var cands []candidate
	add := func(label, p string) {
		if p != "" {
			cands = append(cands, candidate{label, p})
		}
	}
	add("argument", mediaArg)
	add("MEDIA_DIR", envMediaDir)
	if homeErr == nil {
		add("~/Bajadas", filepath.Join(home, "Bajadas"))
		add("~/Documents/Bajadas", filepath.Join(home, "Documents", "Bajadas"))
	}

	var searched []string
	for _, c := range cands {
		abs, err := filepath.Abs(expandHome(c.path, home, homeErr == nil))
		if err != nil {
			searched = append(searched, fmt.Sprintf("%s (%s)", c.label, c.path))
			continue
		}
		searched = append(searched, fmt.Sprintf("%s (%s)", c.label, abs))
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return abs, nil
		}
	}
	if len(searched) == 0 {
		return "", fmt.Errorf("no media directory found: nothing searched; pass a folder as the argument or set MEDIA_DIR")
	}
	return "", fmt.Errorf("no media directory found; searched %s; pass a folder as the argument or set MEDIA_DIR",
		strings.Join(searched, ", "))
}

// expandHome replaces a leading "~" or "~/" with home.
func expandHome(p, home string, haveHome bool) string {
	if haveHome && (p == "~" || strings.HasPrefix(p, "~/")) {
		return filepath.Join(home, p[1:])
	}
	return p
}

// parseDotEnv reads KEY=value lines from path. Blank lines and lines starting
// with # are ignored, surrounding matching quotes are stripped, and the first
// occurrence of a key wins. A missing file yields an empty map.
func parseDotEnv(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		m := envLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if _, seen := out[m[1]]; seen {
			continue
		}
		out[m[1]] = unquote(m[2])
	}
	return out
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
