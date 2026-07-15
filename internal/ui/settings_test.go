package ui

import (
	"encoding/json"
	"os"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	tsync "task-timer-app/internal/sync"

	// The settings screen is built from the provider registry, so a test of it
	// needs providers registered — exactly as a binary's main does it. This is
	// the only reason a provider is named here; internal/ui itself imports none,
	// which TestAppDoesNotDependOnAnyProvider enforces.
	//
	// The gateway is included so the render test lays out a connectable provider's
	// card, with its Log in button, exactly as the real app does.
	_ "task-timer-app/internal/sync/providers/gateway"
	_ "task-timer-app/internal/sync/providers/jsonfile"
)

// TestSettingsRendersEveryRegisteredProvider checks the screen is driven by the
// registry rather than by a hard-coded list. Whatever backends are linked in get
// a form; none are special-cased.
func TestSettingsRendersEveryRegisteredProvider(t *testing.T) {
	store := seedStore(t)
	a := newWithApp(test.NewApp(), store)

	forms := a.settings.providers
	if len(forms) != len(tsync.Descriptors()) {
		t.Fatalf("rendered %d provider forms, but %d providers are registered",
			len(forms), len(tsync.Descriptors()))
	}

	for _, form := range forms {
		if len(form.controls) != len(form.reg.Fields) {
			t.Errorf("provider %q declares %d fields but rendered %d controls",
				form.reg.Name, len(form.reg.Fields), len(form.controls))
		}
		for _, field := range form.reg.Fields {
			if form.controls[field.Key] == nil {
				t.Errorf("provider %q declared field %q but no control was rendered",
					form.reg.Name, field.Key)
			}
		}
	}
}

// TestSettingsSavePreservesUndeclaredKeys is the security-relevant one.
//
// The gateway supports an inline `api_token`, but deliberately does not declare
// it as a Field, so it never appears on screen. A save must therefore write the
// config back without dropping it — otherwise opening the Settings page and
// pressing Save would silently destroy a working daemon's credentials.
func TestSettingsSavePreservesUndeclaredKeys(t *testing.T) {
	store := seedStore(t) // also points TASK_TIMER_DATA_DIR at a temp directory

	// A config as a hand-editing user might have left it: an inline token, and a
	// key from some future version this build knows nothing about.
	original := `{
	  "poll_interval": "60s",
	  "providers": [
	    {
	      "name": "gateway",
	      "enabled": true,
	      "settings": {
	        "base_url": "https://tasktimer.example.com",
	        "api_token": "super-secret-token",
	        "some_future_key": "keep me"
	      }
	    }
	  ]
	}`
	if err := os.WriteFile(tsync.ConfigPath(), []byte(original), 0o600); err != nil {
		t.Fatalf("seeding the config: %v", err)
	}

	a := newWithApp(test.NewApp(), store)
	a.settings.refresh()

	// The user edits something the form *does* render, then saves.
	gw := providerFormNamed(t, a, "gateway")
	gw.controls["base_url"].(*widget.Entry).SetText("https://edited.example.com")
	a.settings.save()

	// Read the file back the way the daemon would.
	cfg, err := tsync.LoadConfig(tsync.ConfigPath())
	if err != nil {
		t.Fatalf("reloading the config: %v", err)
	}

	var block tsync.ProviderConfig
	for _, p := range cfg.Providers {
		if p.Name == "gateway" {
			block = p
		}
	}

	settings := map[string]any{}
	if err := json.Unmarshal(block.Settings, &settings); err != nil {
		t.Fatalf("decoding the saved gateway settings: %v", err)
	}

	if got := settings["api_token"]; got != "super-secret-token" {
		t.Errorf("the inline api_token was destroyed by a save: got %v.\n"+
			"Keys the form does not render must survive a round trip.", got)
	}
	if got := settings["some_future_key"]; got != "keep me" {
		t.Errorf("an unrecognised key was destroyed by a save: got %v", got)
	}
	if got := settings["base_url"]; got != "https://edited.example.com" {
		t.Errorf("the edit was not saved: base_url = %v", got)
	}
	if !block.Enabled {
		t.Error("the provider was left enabled in the file but saved as disabled")
	}
}

// TestSettingsSaveAddsAProviderMissingFromTheConfig covers the upgrade path: a
// build that gains a backend must offer it, even against a config file written
// before that backend existed.
func TestSettingsSaveAddsAProviderMissingFromTheConfig(t *testing.T) {
	store := seedStore(t)

	// A config that predates the jsonfile provider entirely.
	original := `{"poll_interval": "60s", "providers": [{"name": "gateway", "enabled": false, "settings": {}}]}`
	if err := os.WriteFile(tsync.ConfigPath(), []byte(original), 0o600); err != nil {
		t.Fatalf("seeding the config: %v", err)
	}

	a := newWithApp(test.NewApp(), store)
	a.settings.refresh()

	jsonfile := providerFormNamed(t, a, "jsonfile")
	jsonfile.enabled.SetChecked(true)
	jsonfile.controls["dir"].(*widget.Entry).SetText("/tmp/exchange")
	a.settings.save()

	cfg, err := tsync.LoadConfig(tsync.ConfigPath())
	if err != nil {
		t.Fatalf("reloading the config: %v", err)
	}

	for _, p := range cfg.Providers {
		if p.Name != "jsonfile" {
			continue
		}
		if !p.Enabled {
			t.Fatal("jsonfile was appended to the config but not enabled")
		}
		return
	}
	t.Fatal("jsonfile is compiled in and was enabled, but never reached the config file")
}

func providerFormNamed(t *testing.T, a *App, name string) *providerForm {
	t.Helper()

	for _, form := range a.settings.providers {
		if form.reg.Name == name {
			return form
		}
	}
	t.Fatalf("no rendered form for provider %q", name)
	return nil
}
