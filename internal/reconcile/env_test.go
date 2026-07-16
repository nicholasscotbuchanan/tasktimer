package reconcile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnv(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), EnvFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing env file: %v", err)
	}
	return path
}

func TestLoadEnv(t *testing.T) {
	path := writeEnv(t, `
# A comment, and a blank line above.
TASK_TIMER_GATEWAY_TOKEN=secret-token

# 'export' is what people paste in from a shell.
export LINEAR_API_KEY=linear-key

QUOTED="quoted value"
SINGLE='single value'
SPACED  =  padded
`)

	// t.Setenv registers cleanup, so the process env is restored afterwards.
	t.Setenv("UNRELATED", "x")

	names, err := LoadEnv(path)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if len(names) != 5 {
		t.Errorf("set %d variables (%v), want 5", len(names), names)
	}

	for _, tc := range []struct{ key, want string }{
		{"TASK_TIMER_GATEWAY_TOKEN", "secret-token"},
		{"LINEAR_API_KEY", "linear-key"},
		{"QUOTED", "quoted value"},
		{"SINGLE", "single value"},
		{"SPACED", "padded"},
	} {
		if got := os.Getenv(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
		t.Setenv(tc.key, "") // ensure cleanup restores the environment
	}
}

// TestLoadEnvDoesNotOverrideTheEnvironment: an operator who exports a value at
// launch is making a deliberate override, and a file on disk must not silently
// undo it.
func TestLoadEnvDoesNotOverrideTheEnvironment(t *testing.T) {
	path := writeEnv(t, "TASK_TIMER_GATEWAY_TOKEN=from-file\n")

	t.Setenv("TASK_TIMER_GATEWAY_TOKEN", "from-environment")

	names, err := LoadEnv(path)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("LoadEnv reported setting %v; it must skip variables already set", names)
	}
	if got := os.Getenv("TASK_TIMER_GATEWAY_TOKEN"); got != "from-environment" {
		t.Errorf("TASK_TIMER_GATEWAY_TOKEN = %q; the file overwrote the environment", got)
	}
}

// TestLoadEnvMissingFileIsNotAnError: the file is optional, and the overwhelming
// majority of users will never have one.
func TestLoadEnvMissingFileIsNotAnError(t *testing.T) {
	names, err := LoadEnv(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Errorf("a missing env file is not an error, got: %v", err)
	}
	if names != nil {
		t.Errorf("got %v, want no variables", names)
	}
}

func TestLoadEnvRejectsMalformedLines(t *testing.T) {
	path := writeEnv(t, "TASK_TIMER_GATEWAY_TOKEN=fine\nthis line has no equals sign\n")

	if _, err := LoadEnv(path); err == nil {
		t.Error("a line with no '=' should be an error, not silently skipped: " +
			"a typo in a token file must be loud, or the daemon fails later with a 401")
	}
}

// TestEnvFileIsExposed guards the warning about a token file other users can
// read.
func TestEnvFileIsExposed(t *testing.T) {
	path := writeEnv(t, "TASK_TIMER_GATEWAY_TOKEN=secret\n")

	if EnvFileIsExposed(path) {
		t.Error("a 0600 file is not exposed")
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if !EnvFileIsExposed(path) {
		t.Error("a world-readable token file must be reported as exposed")
	}

	if EnvFileIsExposed(filepath.Join(t.TempDir(), "absent.env")) {
		t.Error("a missing file must not be reported as exposed")
	}
}
