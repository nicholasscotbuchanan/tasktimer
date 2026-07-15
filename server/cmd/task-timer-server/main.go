// Command task-timer-server is the Task Timer gateway.
//
// It is the SERVER: desktop clients push their timed work sessions here and this
// service writes them to Jira, each user acting through their own Atlassian
// grant. The desktop application is a separate program in a separate package.
//
// Run with no arguments to start the service (this is what the systemd unit and
// the Windows Service Control Manager do). Subcommands:
//
//   - gen-key   print a fresh AES-256 key for the token-encryption-key file
//   - version   print the build version
//   - service   Windows only: install|uninstall|start|stop the Windows service
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"task-timer-server/internal/api"
	"task-timer-server/internal/config"
	"task-timer-server/internal/crypto"
	"task-timer-server/internal/store"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "gen-key":
			genKey()
			return
		case "version", "-version", "--version":
			fmt.Println(version)
			return
		case "service":
			if err := serviceControl(os.Args[2:]); err != nil {
				log.Fatalf("task-timer-server: %v", err)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q; usage: task-timer-server [gen-key|version|service <install|uninstall|start|stop>]\n", os.Args[1])
			os.Exit(2)
		}
	}

	// Under the Windows Service Control Manager, hand control to the service
	// runner, which drives run() and turns the SCM Stop control into a context
	// cancel. On every other OS - and on Windows when launched from a console -
	// this reports handled=false and we fall through to the foreground path.
	if handled, err := runService(); handled {
		if err != nil {
			log.Fatalf("task-timer-server: %v", err)
		}
		return
	}

	// In the macOS .app build, hand off to the menu bar UI, which runs the server
	// and shows the address to configure it at. Every other build is headless and
	// this reports handled=false, so we fall through to the foreground path.
	if handled, err := runTray(run); handled {
		if err != nil {
			log.Fatalf("task-timer-server: %v", err)
		}
		return
	}

	// Foreground: stop cleanly on SIGINT/SIGTERM so `systemctl stop` (or Ctrl-C)
	// does not sever an in-flight push to Jira mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatalf("task-timer-server: %v", err)
	}
}

// run starts the gateway and blocks until ctx is cancelled, then shuts the HTTP
// server down gracefully. The caller owns ctx: the foreground path ties it to
// SIGINT/SIGTERM, and the Windows service ties it to the SCM Stop control, so
// both get the same graceful drain.
func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Fail at boot, not on the first user's login. A service that starts happily
	// and then 500s on anyone who connects looks healthy to every monitor.
	if err := cfg.RequireOAuth(); err != nil {
		return err
	}

	cipher, err := crypto.NewCipher(cfg.TokenEncryptionKey)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           api.New(st, cfg, cipher).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.Printf("task-timer-server %s listening on %s", version, cfg.Addr())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// genKey prints a fresh encryption key. The operator redirects it into
// /etc/task-timer-server/token_encryption_key, chmod 600, and never regenerates
// it: it decrypts the stored Jira refresh tokens, and a new key silently
// invalidates every one of them.
func genKey() {
	key, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("gen-key: %v", err)
	}
	fmt.Println(key)
}
