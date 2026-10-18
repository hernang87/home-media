// Command mediad is the media server daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"homemedia/internal/api"
	"homemedia/internal/cast"
	"homemedia/internal/config"
	"homemedia/internal/discovery"
	"homemedia/internal/library"
	"homemedia/internal/logbuf"
	"homemedia/internal/metadata"
	"homemedia/internal/transcode"
)

const version = "0.1.0"

func main() {
	logs := logbuf.New(2000)
	// Not slog.Default().Handler(): after SetDefault that would log into itself.
	slog.SetDefault(slog.New(logs.Handler(slog.NewTextHandler(os.Stderr, nil))))
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "doctor":
		err = runDoctor(os.Stdout)
	case "install":
		err = runInstall(os.Stdout)
	case "uninstall":
		err = runUninstall(os.Stdout)
	default: // anything else is the media dir
		err = run(logs)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logs *logbuf.Buffer) error {
	arg := ""
	if len(os.Args) > 1 {
		arg = os.Args[1]
	}
	cfg, err := config.Load(arg)
	if err != nil {
		return err
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if err := checkBinary(bin); err != nil {
			return fmt.Errorf("%s is required (brew install ffmpeg): %w", bin, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	meta := &metadata.Client{APIKey: cfg.TMDBAPIKey, Language: cfg.TMDBLanguage}
	lib := library.New(cfg, meta)
	tx, err := transcode.NewManager(cfg.TmpDir)
	if err != nil {
		return err
	}
	defer tx.StopAll()

	lan := config.LANAddress()
	base := fmt.Sprintf("http://%s:%d", lan, cfg.Port)
	caster := cast.NewManager(lib, base)
	caster.StopHook = tx.StopItem
	handler := api.New(api.Deps{
		Config: cfg, Lib: lib, TX: tx,
		Info:  api.Info{Name: cfg.ServerName, LAN: lan, Port: cfg.Port, Base: base, TMDB: meta.Enabled(), Version: version},
		Extra: caster.Routes,
		Logs:  logs,
	})
	srv := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", cfg.Port), Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	slog.Info("serving", "name", cfg.ServerName, "media", cfg.MediaDir, "url", base)
	if !meta.Enabled() {
		slog.Info("no TMDB_API_KEY set: using video frames as artwork (see .env.example)")
	}

	go caster.Run(ctx)
	go func() {
		if err := discovery.Advertise(ctx, cfg.ServerName, cfg.Port); err != nil {
			slog.Warn("mDNS advertise failed", "err", err)
		}
	}()
	go func() {
		if err := lib.Scan(ctx, false); err != nil && ctx.Err() == nil {
			slog.Error("scan failed", "err", err)
			return
		}
		slog.Info("library scan finished")
	}()
	go func() {
		if err := lib.Watch(ctx); err != nil {
			slog.Warn("file watching disabled", "err", err)
		}
	}()

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdown)
	lib.Flush()
	if errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}
