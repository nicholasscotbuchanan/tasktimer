package sync

import (
	"context"
	"fmt"
)

// Identity is who a backend reports the connected user to be. The desktop app
// surfaces it after a successful Connect so the user sees which account the
// machine was linked to.
type Identity struct {
	Email       string
	DisplayName string
	SiteURL     string
}

// Connector signs a machine in to a backend at baseURL and persists whatever
// credential the daemon needs, returning who was connected. Providers that
// support interactive sign-in register one on their Registration.
//
// The flow is expected to be long-running — it typically opens a browser and
// waits for the user to consent — so callers run it off the UI goroutine and
// pass a context they can cancel.
type Connector func(ctx context.Context, baseURL string) (Identity, error)

// Connect runs the named provider's interactive sign-in against baseURL. It is
// how the desktop app connects to a backend without importing it: the provider
// registered the flow, and this dispatches to it by name, keeping the app free
// of any knowledge of a particular backend.
func Connect(ctx context.Context, name, baseURL string) (Identity, error) {
	r, ok := Describe(name)
	if !ok {
		return Identity{}, fmt.Errorf("unknown provider %q (compiled-in providers: %v)", name, Registered())
	}
	if r.Connect == nil {
		return Identity{}, fmt.Errorf("provider %q does not support interactive sign-in", name)
	}
	return r.Connect(ctx, baseURL)
}

// Connectable returns the registrations of every compiled-in provider that
// offers interactive sign-in, ordered by name. The app walks this to decide
// whether it can offer a Connect flow at all.
func Connectable() []Registration {
	out := make([]Registration, 0)
	for _, r := range Descriptors() {
		if r.Connectable() {
			out = append(out, r)
		}
	}
	return out
}
