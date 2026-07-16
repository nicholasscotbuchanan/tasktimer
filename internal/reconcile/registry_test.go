package reconcile

import (
	"encoding/json"
	"testing"
	"time"
)

// TestDefaultPollIntervalTextMatches keeps the human-readable default in step
// with the duration it is supposed to spell.
func TestDefaultPollIntervalTextMatches(t *testing.T) {
	parsed, err := time.ParseDuration(DefaultPollIntervalText)
	if err != nil {
		t.Fatalf("DefaultPollIntervalText %q is not a duration: %v", DefaultPollIntervalText, err)
	}
	if parsed != DefaultPollInterval {
		t.Errorf("DefaultPollIntervalText is %q (%s) but DefaultPollInterval is %s",
			DefaultPollIntervalText, parsed, DefaultPollInterval)
	}
}

// TestDefaultSettingsRendersDeclaredFields checks that a provider's declared
// defaults become a usable settings block. This is what seeds a starter config
// for a backend nobody has configured yet, so a provider that declares fields
// must never produce an empty object.
func TestDefaultSettingsRendersDeclaredFields(t *testing.T) {
	const name = "descriptor-test-provider"

	Register(Registration{
		Name:  name,
		Title: "Descriptor Test",
		New: func(json.RawMessage) (Provider, error) {
			return nil, ErrUnsupported
		},
	})

	// The settings schema now lives in providers.yaml, loaded once. Force that
	// load, then declare a schema for the test provider the same way the file
	// would — this is what Describe/Descriptors hand back.
	_ = Descriptors()
	schemaFields[name] = []Field{
		{Key: "endpoint", Kind: KindText, Default: "https://example.invalid"},
		{Key: "verbose", Kind: KindBool, Default: true},
		{Key: "empty", Kind: KindText},
	}

	var settings map[string]any
	if err := json.Unmarshal(DefaultSettings(name), &settings); err != nil {
		t.Fatalf("DefaultSettings produced invalid JSON: %v", err)
	}

	if got := settings["endpoint"]; got != "https://example.invalid" {
		t.Errorf("endpoint = %v, want the declared default", got)
	}
	if got := settings["verbose"]; got != true {
		t.Errorf("verbose = %v, want true — a bool default must survive as a JSON bool", got)
	}
	if got, ok := settings["empty"]; !ok || got != "" {
		t.Errorf("empty = %v (present=%v), want an empty string; a field with no default "+
			"must still appear, or the user cannot see it exists", got, ok)
	}
}

// TestExampleConfigCoversEveryRegisteredProvider is the guard on the starter
// file. The config layer must not know its plugins by name: whatever is in the
// registry has to appear, disabled, so a newly compiled-in backend is visible to
// someone who has never read the source.
func TestExampleConfigCoversEveryRegisteredProvider(t *testing.T) {
	cfg := exampleConfig()

	present := make(map[string]ProviderConfig, len(cfg.Providers))
	for _, p := range cfg.Providers {
		present[p.Name] = p
	}

	for _, name := range Registered() {
		block, ok := present[name]
		if !ok {
			t.Errorf("provider %q is registered but missing from the starter config", name)
			continue
		}
		if block.Enabled {
			t.Errorf("provider %q is enabled in the starter config; a first run must be a no-op", name)
		}
		if len(block.Settings) == 0 {
			t.Errorf("provider %q has no settings block in the starter config", name)
		}
	}

	if _, err := cfg.Interval(); err != nil {
		t.Errorf("the starter config does not parse its own poll interval: %v", err)
	}
}

// TestRegisterRejectsIncompleteRegistrations: a provider with no name or no
// factory is a programming error, and must blow up at startup rather than
// registering something the engine will later fail to build.
func TestRegisterRejectsIncompleteRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name string
		reg  Registration
	}{
		{"no name", Registration{New: func(json.RawMessage) (Provider, error) { return nil, nil }}},
		{"no factory", Registration{Name: "nameless-factory"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%+v) did not panic", tc.reg)
				}
			}()
			Register(tc.reg)
		})
	}
}
