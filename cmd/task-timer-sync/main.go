// Command task-timer-sync is the headless daemon that keeps the local timer
// database in step with external task repositories.
//
// Backends are pluggable. Each provider package registers itself under a name;
// this file blank-imports the ones compiled into the binary, and the config
// file at <data-dir>/sync.json decides which of them actually run. To add a
// backend, implement internal/sync.Provider and add one import line here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tsync "task-timer-app/internal/sync"
	"task-timer-app/internal/task"

	"task-timer-app/internal/sync/providers/gateway"

	// Compiled-in providers. Blank imports; they register themselves.
	_ "task-timer-app/internal/sync/providers/jsonfile"
)

func main() {
	var (
		configPath = flag.String("config", tsync.ConfigPath(), "path to the sync config file")
		once       = flag.Bool("once", false, "run a single sync cycle and exit")
		list       = flag.Bool("providers", false, "list the compiled-in providers and exit")
		connect    = flag.Bool("connect", false, "sign in to the gateway in a browser, then exit")
	)
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags)

	if *list {
		for _, name := range tsync.Registered() {
			fmt.Println(name)
		}
		return
	}

	if *connect {
		if err := connectGateway(*configPath); err != nil {
			logger.Fatalf("task-timer-sync: %v", err)
		}
		return
	}

	// Before anything reads an API token from the environment. A daemon started
	// by systemd or launchd inherits none of the shell's exports, so without this
	// a setup that works from a terminal fails as a service with an opaque 401.
	loadEnvFile(logger)

	if err := run(*configPath, *once, logger); err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Print("shutting down")
			return
		}
		logger.Fatalf("task-timer-sync: %v", err)
	}
}

// loadEnvFile pulls <data-dir>/sync.env into the environment, if it exists.
//
// Only the variable names are logged. The values are API tokens, and the whole
// point of keeping them out of the config file is that they do not end up
// somewhere they can be read by accident — a log file being exactly that.
func loadEnvFile(logger *log.Logger) {
	path := tsync.EnvPath()

	names, err := tsync.LoadEnv(path)
	if err != nil {
		logger.Printf("warning: %v", err)
		return
	}
	if len(names) == 0 {
		return
	}

	logger.Printf("env:      %s (%s)", path, strings.Join(names, ", "))

	if tsync.EnvFileIsExposed(path) {
		logger.Printf("warning: %s is readable by other users; it holds API tokens. chmod 600 it.", path)
	}
}

// connectGateway signs this machine in to the gateway and stores the bearer
// token, then exits. It is the one-time setup step, and it is a flag on the
// daemon rather than a separate binary so that a headless install — a box with
// no desktop app on it at all — can still be connected from a terminal.
//
// The token is written to sync.env, which is the same file the daemon reads at
// startup, so the next cycle picks it up with no further ceremony.
func connectGateway(configPath string) error {
	cfg, err := tsync.LoadConfig(configPath)
	if err != nil {
		return err
	}

	var gw gateway.Config
	found := false
	for _, pc := range cfg.Providers {
		if pc.Name != gateway.ProviderName {
			continue
		}
		if len(pc.Settings) > 0 {
			if err := json.Unmarshal(pc.Settings, &gw); err != nil {
				return fmt.Errorf("reading the gateway settings in %s: %w", configPath, err)
			}
		}
		found = true
		break
	}
	if !found {
		return fmt.Errorf(
			"no %q provider in %s. Add it and set its base_url to your gateway, "+
				"or set the Gateway URL on the app's Settings page, then run this again.",
			gateway.ProviderName, configPath)
	}
	if strings.TrimSpace(gw.BaseURL) == "" {
		return fmt.Errorf("the gateway's base_url is not set in %s", configPath)
	}

	fmt.Printf("Connecting to %s ...\n", gw.BaseURL)

	token, who, err := gateway.Connect(context.Background(), gw.BaseURL)
	if err != nil {
		return err
	}

	path, err := gateway.SaveToken(gw, token)
	if err != nil {
		return err
	}

	name := who.DisplayName
	if name == "" {
		name = who.Email
	}
	fmt.Printf("\nConnected as %s.\n", name)
	if who.SiteURL != "" {
		fmt.Printf("Tracker site: %s\n", who.SiteURL)
	}
	fmt.Printf("Token saved: %s (mode 0600)\n", path)
	return nil
}

func run(configPath string, once bool, logger *log.Logger) error {
	cfg, err := tsync.LoadConfig(configPath)
	if err != nil {
		return err
	}

	interval, err := cfg.Interval()
	if err != nil {
		return err
	}

	store, err := task.Open()
	if err != nil {
		return err
	}
	defer store.Close()

	logger.Printf("database: %s", task.DBPath())
	logger.Printf("config:   %s", configPath)

	engine, err := tsync.NewEngine(store, cfg, logger)
	if err != nil {
		return err
	}

	if once {
		engine.RunOnce(context.Background())
		return nil
	}

	// Cancel on SIGINT/SIGTERM so an in-flight cycle is allowed to notice and
	// stop between providers rather than being killed mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Printf("syncing every %s", interval)
	return engine.Run(ctx, interval)
}
