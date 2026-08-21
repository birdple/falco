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
	defaultName string
}

// NewRegistry creates a new storage registry with a default backend.
func NewRegistry(defaultBackend StorageBackend) *Registry {
	r := &Registry{
		backends:    make(map[string]StorageBackend),
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

// Get returns the backend registered under the given name.
// Returns the default backend if name is empty.
func (r *Registry) Get(name string) (StorageBackend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if name == "" {
		name = r.defaultName
	}
	backend, ok := r.backends[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBackendNotFound, name)
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

// Closer lo implementan los backends que dejan trabajo en vuelo y hay que
// esperar al apagar (hoy: ReplicatedStorage con targets async).
type Closer interface {
	Close(ctx context.Context) error
}

// CloseAll espera a los backends registrados que tengan trabajo pendiente.
// Devuelve un mapa nombre → error con SÓLO los que fallaron; vacío significa
// que todos terminaron limpio. Los backends que no implementan Closer no
// aparecen: no tienen nada que esperar.
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
