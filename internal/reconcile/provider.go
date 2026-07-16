// Package reconcile keeps locally timed work sessions in step with external task
// repositories reached through the backend.
//
// A backend is anything that satisfies Provider. Providers register themselves
// under a name in their package's init function, and the engine instantiates
// whichever ones the config file enables. Adding a new backend — GitHub Issues,
// Linear, Asana — means writing one file that implements Provider and calling
// Register; nothing in the engine, the store, or the app changes.
//
// A provider's *settings schema* — the fields it exposes, their labels and
// defaults — is not compiled in. It lives in providers.yaml (seeded from the
// embedded copy on first run) and is loaded at runtime, so what a backend can be
// configured with is data a person can edit, not Go source.
package reconcile

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"task-timer-app/internal/task"
)

// ErrUnsupported is returned by optional Provider methods that a given backend
// does not implement. The engine treats it as a skip, not a failure — a
// read-only provider is a legitimate provider.
var ErrUnsupported = errors.New("operation not supported by this provider")

// WorkLog is a completed local timer session being pushed to a provider.
type WorkLog struct {
	// Key identifies the task in the provider, e.g. a remote issue key.
	Key string
	// Started is when the user began the session.
	Started time.Time
	// Duration is how long the session ran.
	Duration time.Duration
	// Comment is optional free text to attach to the work log.
	Comment string
	// Author is the local user who recorded the session.
	Author string
}

// Provider is a reconciliation backend: something that can supply tasks and
// accept the time spent on them.
//
// Pull and Push are independent capabilities. A provider may implement only
// one; return ErrUnsupported from the other and the engine will skip that half
// of the cycle.
type Provider interface {
	// Name is the provider's registered identifier. It is stored in the
	// task_source column, so it must be stable across releases.
	Name() string

	// Pull returns tasks assigned to the user that changed at or after `since`.
	// A zero `since` means a full pull. Providers should return tasks in any
	// order; the engine does not depend on it.
	Pull(ctx context.Context, since time.Time) ([]task.Remote, error)

	// Push sends a completed work session to the provider and returns the
	// provider's identifier for the created work log. That identifier is
	// persisted, and is what stops the same session being pushed twice.
	Push(ctx context.Context, wl WorkLog) (string, error)

	// Complete tells the provider the task is finished, typically by
	// transitioning it to a done state. Providers that cannot or should not
	// mutate task state return ErrUnsupported.
	Complete(ctx context.Context, key string) error
}

// Factory builds a Provider from the raw JSON of its config block. It is called
// once per enabled provider at daemon start.
type Factory func(cfg json.RawMessage) (Provider, error)

// Kind is how a setting should be presented and stored.
type Kind int

const (
	// KindText is a free-text JSON string.
	KindText Kind = iota
	// KindBool is a JSON boolean, shown as a checkbox.
	KindBool
)

// Field describes one key inside a provider's settings block.
//
// It exists so that a user interface can render a complete, correct form for a
// backend it has never heard of. Without it, every new provider would mean
// editing the settings screen — and a plugin system whose host has to be
// modified for each plugin is not a plugin system.
//
// A setting the provider supports but does not declare here is simply not
// offered by the UI, which is deliberate: it is how a backend keeps its inline
// `api_token` editable by hand but off the screen. Anything a form does not
// know about is preserved untouched when the config is written back.
type Field struct {
	// Key is the JSON key inside the provider's settings object.
	Key string
	// Label is the human name for the setting.
	Label string
	// Hint is an optional one-line explanation shown beneath the label.
	Hint string
	// Kind decides the control and the JSON type.
	Kind Kind
	// Placeholder is example text shown in an empty text field.
	Placeholder string
	// Default seeds a starter config. It must be a string for KindText and a
	// bool for KindBool.
	Default any
}

// Registration is everything the engine and the app need to know about a
// backend: how to build it, and how to configure it.
type Registration struct {
	// Name is the stable identifier written to the config file and to the
	// task_source column.
	Name string
	// Title is the human name, e.g. "Task Timer Gateway".
	Title string
	// Summary is a one-line description shown above the provider's settings.
	Summary string
	// New builds the provider from its config block.
	New Factory
	// Fields declares the provider's settings. Providers no longer set this
	// themselves: it is populated from providers.yaml when a registration is
	// handed out by Describe/Descriptors, so the settings schema is data on disk
	// rather than Go source. A provider with no entry in providers.yaml simply
	// has no configurable settings.
	Fields []Field
	// URLField names the settings key that holds the backend's base URL, when it
	// has one. The app uses it to pre-fill and persist the URL from the Connect
	// dialog without knowing the provider's other settings. Empty for providers
	// that have no single connectable endpoint.
	URLField string
	// Connect performs interactive sign-in against a base URL and stores the
	// credential the daemon will use. Providers without a sign-in flow leave it
	// nil, and the app only offers Connect for providers that set it.
	Connect Connector
	// HasToken reports whether a stored credential already exists, so the app can
	// skip sign-in when the machine is already connected. Optional; a nil value
	// reads as "not known to be connected".
	HasToken func() bool
}

// Connectable reports whether this provider offers interactive sign-in.
func (r Registration) Connectable() bool { return r.Connect != nil }

var (
	registryMu sync.RWMutex
	registry   = map[string]Registration{}
)

// SchemaFileName is the provider settings schema, in the data directory beside
// config.yaml.
const SchemaFileName = "providers.yaml"

// seedSchema is the built-in schema, written to the data directory on first run
// so there is always a concrete, editable file to point people at.
//
//go:embed providers.yaml
var seedSchema []byte

var (
	schemaOnce   sync.Once
	schemaFields map[string][]Field
)

// SchemaPath returns the location of the provider schema file.
func SchemaPath() string {
	return filepath.Join(task.DataDir(), SchemaFileName)
}

// fieldYAML is one field as written in providers.yaml. Kind is a word there
// ("text"/"bool") rather than the internal enum, so the file reads for humans.
type fieldYAML struct {
	Key         string `yaml:"key"`
	Label       string `yaml:"label"`
	Hint        string `yaml:"hint"`
	Kind        string `yaml:"kind"`
	Placeholder string `yaml:"placeholder"`
	Default     any    `yaml:"default"`
}

// fieldsFor returns the settings schema declared for a provider in providers.yaml,
// loading the file once. A missing or unreadable file falls back to the embedded
// seed, so the app is always configurable even if the on-disk copy is deleted.
func fieldsFor(name string) []Field {
	schemaOnce.Do(loadSchema)
	return schemaFields[name]
}

// loadSchema is read-only: it never writes. It reads the on-disk providers.yaml
// if present, otherwise parses the embedded seed. The editable on-disk copy is
// created separately by WriteSchemaSeed at startup, so nothing on a read path
// (Descriptors, called all over the UI) has a filesystem side effect.
func loadSchema() {
	schemaFields = map[string][]Field{}

	data := seedSchema
	if onDisk, err := os.ReadFile(SchemaPath()); err == nil {
		data = onDisk
	}

	var raw map[string][]fieldYAML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// A broken on-disk file should not blank the whole UI; fall back to the seed.
		if err := yaml.Unmarshal(seedSchema, &raw); err != nil {
			return
		}
	}

	for name, fields := range raw {
		out := make([]Field, 0, len(fields))
		for _, f := range fields {
			out = append(out, f.toField())
		}
		schemaFields[name] = out
	}
}

// WriteSchemaSeed writes the embedded providers.yaml into the data directory if
// no copy is there yet, so a user has a concrete file to edit. It is called once
// at startup by each binary; an existing file — including one the user has
// edited — is never overwritten.
func WriteSchemaSeed() error {
	path := SchemaPath()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	if err := os.WriteFile(path, seedSchema, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// toField converts a YAML field into the internal Field, mapping the kind word
// onto the enum and coercing the default to the type the kind implies.
func (f fieldYAML) toField() Field {
	kind := KindText
	if f.Kind == "bool" {
		kind = KindBool
	}
	var def any
	switch kind {
	case KindBool:
		b, _ := f.Default.(bool)
		def = b
	default:
		s, _ := f.Default.(string)
		def = s
	}
	return Field{
		Key:         f.Key,
		Label:       f.Label,
		Hint:        f.Hint,
		Kind:        kind,
		Placeholder: f.Placeholder,
		Default:     def,
	}
}

// Register makes a provider available to the engine and to the app's settings
// screen. Providers call this from an init function; the binaries blank-import
// the provider packages they want compiled in. Registering the same name twice
// is a programming error and panics at startup rather than silently shadowing.
func Register(r Registration) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if r.Name == "" {
		panic("reconcile: provider registered without a name")
	}
	if r.New == nil {
		panic(fmt.Sprintf("reconcile: provider %q registered without a factory", r.Name))
	}
	if _, exists := registry[r.Name]; exists {
		panic(fmt.Sprintf("reconcile: provider %q registered twice", r.Name))
	}
	registry[r.Name] = r
}

// Registered returns the names of all compiled-in providers, sorted. The daemon
// prints these on an unknown-provider error so a typo in the config file is
// immediately obvious.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Descriptors returns every compiled-in provider's registration, ordered by
// name. The settings screen walks this to build its forms, which is what keeps
// the app free of any knowledge of a particular backend.
func Descriptors() []Registration {
	registryMu.RLock()
	defer registryMu.RUnlock()

	out := make([]Registration, 0, len(registry))
	for _, r := range registry {
		r.Fields = fieldsFor(r.Name)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Describe returns one provider's registration, with its settings schema filled
// in from providers.yaml.
func Describe(name string) (Registration, bool) {
	registryMu.RLock()
	r, ok := registry[name]
	registryMu.RUnlock()

	if !ok {
		return Registration{}, false
	}
	r.Fields = fieldsFor(name)
	return r, true
}

// DefaultSettings renders a provider's declared defaults as a settings block,
// which is what seeds a starter config for a provider nobody has configured yet.
func DefaultSettings(name string) json.RawMessage {
	r, ok := Describe(name)
	if !ok {
		return json.RawMessage(`{}`)
	}

	values := make(map[string]any, len(r.Fields))
	for _, f := range r.Fields {
		switch f.Kind {
		case KindBool:
			b, _ := f.Default.(bool)
			values[f.Key] = b
		default:
			s, _ := f.Default.(string)
			values[f.Key] = s
		}
	}

	raw, err := json.Marshal(values)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// build instantiates a provider by name.
func build(name string, cfg json.RawMessage) (Provider, error) {
	registryMu.RLock()
	r, ok := registry[name]
	registryMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown provider %q (compiled-in providers: %v)", name, Registered())
	}

	p, err := r.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("configuring provider %q: %w", name, err)
	}
	if p.Name() != name {
		return nil, fmt.Errorf("provider registered as %q reports its name as %q", name, p.Name())
	}
	return p, nil
}
