# AGENTS.md

Guide for AI coding agents working in this repo.

## What this is

Go media server (module `homemedia`). `mediad` serves a small phone control page at `/` (LAN only).

- `cmd/mediad`: daemon. Scans `MEDIA_DIR`, serves direct play and HLS transcode (ffmpeg), converts subtitles to WebVTT, casts to Chromecast. The CASTv2 client is hand-rolled in `internal/cast` and uses the stock Default Media Receiver.
- `cmd/homemedia-app`: Fyne macOS desktop client. Talks to mediad over HTTP via `internal/client`.

## Layout

- `internal/model`: types shared across packages
- `internal/config`: settings from flags, env and `.env`
- `internal/parse`: release-style file names to library metadata
- `internal/probe`: ffprobe wrapper; decides what a Chromecast can play
- `internal/subs`: subtitle track names, conversion to WebVTT
- `internal/metadata`: TMDB client and artwork cache
- `internal/library`: folder scan, item cache, watch state, file watching
- `internal/transcode`: on-demand HLS segments via ffmpeg
- `internal/api`: HTTP API and media files (`GET /api/logs?after=<seq>` serves the log ring; `/api/settings` reads and writes settings)
- `internal/logbuf`: in-memory slog ring buffer behind the app's Logs tab
- `internal/web`: embedded phone page (plain HTML/JS in `static/`, no build step; rebuild `mediad` to pick up edits)
- `internal/daemon`: the desktop app starts `mediad` when none answers on 127.0.0.1:8200, and stops it on quit
- `internal/cast`: Chromecast discovery and CASTv2 playback
- `internal/discovery`: mDNS advertise and browse
- `internal/client`: typed HTTP client for mediad
- `internal/ui`: Fyne desktop views
- `internal/ui/vm`: pure view logic, testable without a window

## Commands

```sh
go build -o bin/mediad ./cmd/mediad
go build -o bin/homemedia-app ./cmd/homemedia-app   # needs cgo (Fyne)
go build ./... && go vet ./... && go test ./...
bin/mediad    # run from repo root: reads ./.env, writes ./data
```

- `bin/mediad doctor` checks setup; `bin/mediad install` / `uninstall` manage the launchd agent `com.homemedia.mediad` (run install from the repo root; logs go to `~/Library/Logs/homemedia/mediad.log`).
- `bash scripts/package-app.sh` builds `bin/HomeMedia.app`; `go run ./cmd/genicon cmd/homemedia-app/icon.png` regenerates the icon.
- `ffmpeg` and `ffprobe` must be on PATH (here: `/opt/homebrew/bin`).

## Conventions

- HTTP routes use stdlib `net/http` mux patterns.
- Logging uses `log/slog`.
- Run `gofmt`. Prefer table-driven tests.
- Put pure logic in `internal/ui/vm` so it can be tested without a window.
- Fyne widgets are touched only on the UI thread. From goroutines, use `fyne.Do`.

## Gotchas

- Resume protocol: `POST /api/cast/{device}/play` returns 409 with `{"code":"resume",...}` when saved progress is >= 30 s. The client retries with `"resume":"continue"` or `"resume":"restart"`.
- Progress is saved every 10 s and on pause, stop and finish.
- Without `TMDB_API_KEY`, posters are null and the app falls back to the backdrop.
- Device names in URLs must be percent-encoded, or use the device ID.
- Do not wrap `slog.Default().Handler()` in a handler passed to `slog.SetDefault` (it recurses); build a fresh handler.
- App-owned `mediad` (child of `homemedia-app`) gets `HOMEMEDIA_PARENT_PID` and exits when that process dies, even on a hard kill. A `mediad` already running (launchd, terminal) is used as-is and never stopped by the app; `bin/mediad uninstall` if you want the app to own it.
- `mediad` runs `caffeinate -i -w <pid>` so the Mac does not idle-sleep while it is up.
- The menu bar item (`internal/ui/tray.go`) polls the chosen TV every 3 s, even with the window hidden, and refreshes the menu only when `vm.Tray` changes.
- Audio-only Cast devices (mDNS `ca` bit 0 unset) are hidden.
- Settings writes (`POST /api/settings`) are accepted only from loopback or mediad's own addresses. `GET /api/settings` never returns the TMDB key, only `tmdbKeySet`.
- `.env` is rewritten by `config.UpdateEnvFile` (atomic, mode 0600). It replaces the first occurrence of a key, because `parseDotEnv` is first-wins. Real env vars still override `.env`, and `daemon.Start` always sets `PORT`.
- TMDB client credentials change at runtime. Use `SetCredentials` and `Credentials`; never assign `APIKey` or `Language` after construction.
- `homemedia-app` has a menu bar item (Open HomeMedia, Quit). The window close button only hides the window; Quit (or Cmd-Q) exits and stops an app-owned `mediad`.
- The mDNS round-trip test needs a real LAN and is behind a build tag: `go test -tags mdns ./internal/discovery`. CI runs the fake-feed tests only.
- Never commit `.env`, `data/` or `bin/`.
