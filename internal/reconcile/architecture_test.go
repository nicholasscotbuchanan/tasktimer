package reconcile_test

import (
	"os/exec"
	"strings"
	"testing"
)

// providerPrefix is the import path every backend lives under.
const providerPrefix = "task-timer-app/internal/reconcile/providers/"

// TestAppDoesNotDependOnAnyProvider is the guard on the plugin boundary.
//
// The whole point of the registry is that a backend is a plugin: one package
// that implements Provider and calls Register, plus one blank import in each
// binary's main. Nothing else may know a backend exists.
//
// The interface is the tempting place to break this. An earlier version of the
// settings screen imported a provider package so it could bind a form directly to
// that provider's Config — which compiled, worked, and quietly meant that adding
// Linear or GitHub Issues would require editing the app. A plugin system whose
// host must be modified for each plugin is not a plugin system.
//
// The settings screen is instead built from reconcile.Descriptors(), so this
// test asserts the dependency simply cannot come back. If it fails, the fix is
// not to relax the test: it is to declare the settings as reconcile.Field values
// on the provider, the way gateway and jsonfile do.
func TestAppDoesNotDependOnAnyProvider(t *testing.T) {
	for _, pkg := range []string{
		"task-timer-app/internal/ui",
		"task-timer-app/internal/reconcile",
		"task-timer-app/internal/task",
	} {
		t.Run(pkg, func(t *testing.T) {
			for _, dep := range deps(t, pkg) {
				if strings.HasPrefix(dep, providerPrefix) {
					t.Errorf("%s imports the provider %q.\n\n"+
						"Backends are plugins: only a binary's main may name one. "+
						"If this is the settings screen needing to render the provider's "+
						"configuration, declare it as []sync.Field on the provider's "+
						"Registration instead — the form is built from the registry.",
						pkg, strings.TrimPrefix(dep, providerPrefix))
				}
			}
		})
	}
}

// TestBinariesRegisterTheProviders is the other half: the composition roots must
// actually link the backends in, or the registry is empty and the daemon can
// reconcile nothing while the settings screen shows no providers at all.
func TestBinariesRegisterTheProviders(t *testing.T) {
	for _, bin := range []string{
		"task-timer-app/cmd/task-timer",
		"task-timer-app/cmd/task-timer-daemon",
	} {
		t.Run(bin, func(t *testing.T) {
			var found []string
			for _, dep := range deps(t, bin) {
				if strings.HasPrefix(dep, providerPrefix) {
					found = append(found, dep)
				}
			}
			if len(found) == 0 {
				t.Errorf("%s links no providers, so its registry is empty", bin)
			}
		})
	}
}

// deps returns the full transitive import list of a package.
func deps(t *testing.T, pkg string) []string {
	t.Helper()

	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}
