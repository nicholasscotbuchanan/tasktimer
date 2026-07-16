package gateway

import (
	"os"
	"path/filepath"
	"testing"

	"task-timer-app/internal/reconcile"
)

// TestRegisteredAsConnectable is the wiring the desktop app relies on: the app
// offers a Connect flow only for providers whose registration carries one, and
// pre-fills the URL from the field the registration names.
func TestRegisteredAsConnectable(t *testing.T) {
	reg, ok := reconcile.Describe(ProviderName)
	if !ok {
		t.Fatalf("provider %q is not registered", ProviderName)
	}
	if !reg.Connectable() {
		t.Error("the gateway must register a Connect flow, or the app cannot offer sign-in")
	}
	if reg.URLField != "base_url" {
		t.Errorf("URLField = %q, want base_url so the app pre-fills the right setting", reg.URLField)
	}
	if reg.HasToken == nil {
		t.Error("the gateway must register HasToken, or the app cannot tell it is already connected")
	}
}

// TestHasTokenFromEnvironment: a token exported into the process environment is
// enough — this is how the daemon runs.
func TestHasTokenFromEnvironment(t *testing.T) {
	t.Setenv("TASK_TIMER_DATA_DIR", t.TempDir())
	t.Setenv(defaultTokenEnv, "tt_test_token")

	if !hasToken() {
		t.Error("a token in the environment was not detected")
	}
}

// TestHasTokenFromEnvFile: with nothing in the process environment, a token in
// the daemon's credentials.env must still count as connected — that is where the
// Connect flow writes it.
func TestHasTokenFromEnvFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TASK_TIMER_DATA_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, reconcile.EnvFileName),
		[]byte(defaultTokenEnv+"=tt_from_file\n"), 0o600); err != nil {
		t.Fatalf("seeding %s: %v", reconcile.EnvFileName, err)
	}

	if !hasToken() {
		t.Errorf("a token in %s was not detected", reconcile.EnvFileName)
	}
}

// TestHasTokenIsFalseWhenAbsent: an unconnected machine reads as unconnected, so
// the app knows to offer sign-in.
func TestHasTokenIsFalseWhenAbsent(t *testing.T) {
	t.Setenv("TASK_TIMER_DATA_DIR", t.TempDir())

	if hasToken() {
		t.Error("reported connected with no token anywhere")
	}
}
