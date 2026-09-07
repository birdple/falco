package storage

import (
	"context"
	"fmt"
	"sync"
)

// Registry holds named storage backends and provides lookup by name.
// In single mode it contains one backend under the "default" key.
// In multi mode it can hold any number of named backends.
type Registry struct {
	mu          sync.RWMutex
	backends    map[string]StorageBackend
	aliases     map[string]string
	defaultName string
}

// NewRegistry creates a new storage registry with a default backend.
func NewRegistry(defaultBackend StorageBackend) *Registry {
	r := &Registry{
		backends:    make(map[string]StorageBackend),
		aliases:     make(map[string]string),
		defaultName: "default",
	}
	r.backends["default"] = defaultBackend
	return r
}

// Register adds a named backend to the registry.
func (r *Registry) Register(name string, backend StorageBackend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[name] = backend
}

// SetDefault changes which named backend is used as the default.
func (r *Registry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.backends[name]; !ok {
		return fmt.Errorf("%w: %s", ErrBackendNotFound, name)
	}
	r.defaultName = name
	return nil
}

// RegisterAlias makes `alias` resolve to the backend registered as `target`.
//
// Aliases exist because the name a client asks for and the name an operator
// declared are configured in two different places, and nothing keeps them in
// step: falco's bucket names come from STORAGE_BUCKET_<NAME>_* (so they cannot
// even contain a hyphen), while every consumer hardcodes or configures its own
// string. Before aliases the mismatch was absorbed silently — an upload naming
// an unknown bucket landed in the default one and still answered 201. An alias
// turns that accident into a declaration: the operator states that "birdple-dev"
// means "jay", and anything NOT declared is refused instead of redirected.
//
// Registering an alias over an existing backend name, or pointing one at a
// backend that is not registered, is a configuration error and is refused here
// rather than discovered on the first request.
func (r *Registry) RegisterAlias(alias, target string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if alias == "" || target == "" {
		return fmt.Errorf("%w: alias %q -> %q: both names are required", ErrInvalidConfiguration, alias, target)
	}
	if _, taken := r.backends[alias]; taken {
		return fmt.Errorf("%w: alias %q shadows a registered bucket", ErrInvalidConfiguration, alias)
	}
	if _, ok := r.backends[target]; !ok {
		return fmt.Errorf("%w: alias %q -> %q", ErrBackendNotFound, alias, target)
	}
	r.aliases[alias] = target
	return nil
}

// Canonical returns the registry name that `name` stands for: the name itself
// when it is a registered backend or is unknown, and the alias target when it
// is a declared alias. An empty name canonicalises to the default backend's
// name.
//
// Callers use this BEFORE authorization so that a scope is always evaluated
// against one name per bucket. Checking the alias instead would let the same
// bucket be allowed under one spelling and denied under another.
func (r *Registry) Canonical(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.canonicalLocked(name)
}

// canonicalLocked is Canonical without taking the lock. Aliases are one hop
// deep on purpose: RegisterAlias refuses a target that is not a backend, so an
// alias can never point at another alias.
func (r *Registry) canonicalLocked(name string) string {
	if name == "" {
		return r.defaultName
	}
	if target, ok := r.aliases[name]; ok {
		return target
	}
	return name
}

// Aliases returns a copy of the declared alias -> bucket mapping.
func (r *Registry) Aliases() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.aliases))
	for alias, target := range r.aliases {
		out[alias] = target
	}
	return out
}

// Get returns the backend registered under the given name, resolving a
// declared alias first.
// Returns the default backend if name is empty.
func (r *Registry) Get(name string) (StorageBackend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	requested := name
	name = r.canonicalLocked(name)
	backend, ok := r.backends[name]
	if !ok {
		if requested == "" {
			requested = name
		}
		return nil, fmt.Errorf("%w: %s", ErrBackendNotFound, requested)
	}
	return backend, nil
}

// Default returns the default storage backend.
func (r *Registry) Default() StorageBackend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.backends[r.defaultName]
}

// Names returns a list of all registered backend names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	return names
}

// Len returns the number of registered backends.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.backends)
}

// HealthAll runs health checks on all registered backends.
// Returns a map of backend name to error (nil if healthy).
func (r *Registry) HealthAll(ctx context.Context) map[string]error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make(map[string]error, len(r.backends))
	for name, backend := range r.backends {
		results[name] = backend.Health(ctx)
	}
	return results
}

// Closer is implemented by backends that leave work in flight and have to be
// waited on at shutdown — today, ReplicatedStorage with async targets.
type Closer interface {
	Close(ctx context.Context) error
}

// CloseAll waits on every registered backend that has pending work.
//
// Returns a name → error map containing ONLY the ones that failed; empty means
// they all finished cleanly. Backends that do not implement Closer are absent:
// there is nothing to wait for.
func (r *Registry) CloseAll(ctx context.Context) map[string]error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	failures := make(map[string]error)
	for name, backend := range r.backends {
		closer, ok := backend.(Closer)
		if !ok {
			continue
		}
		if err := closer.Close(ctx); err != nil {
			failures[name] = err
		}
	}
	return failures
}

// DefaultName returns the name of the default backend.
func (r *Registry) DefaultName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultName
}
