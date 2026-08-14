package usecase

import (
	"fmt"
	"sort"
	"sync"

	"uitester/internal/domain"
)

// ToolRegistry holds every ToolConnector the application knows about.
// It is the plugin point: adapters register themselves here (typically in
// main.go's composition root), and Scenarios select one by name at runtime.
type ToolRegistry struct {
	mu         sync.RWMutex
	connectors map[string]domain.ToolConnector
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{connectors: make(map[string]domain.ToolConnector)}
}

// Register adds a connector. Registering two connectors under the same name
// is almost always a config mistake, so it's rejected rather than silently
// overwritten.
func (r *ToolRegistry) Register(c domain.ToolConnector) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := c.Name()
	if name == "" {
		return fmt.Errorf("tool connector has empty name")
	}
	if _, exists := r.connectors[name]; exists {
		return fmt.Errorf("tool connector %q already registered", name)
	}
	r.connectors[name] = c
	return nil
}

// Resolve looks up a connector by name.
func (r *ToolRegistry) Resolve(name string) (domain.ToolConnector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q (known: %v)", domain.ErrToolNotRegistered, name, r.namesLocked())
	}
	return c, nil
}

// Names lists every registered tool name, sorted, mainly for CLI help/errors.
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.namesLocked()
}

func (r *ToolRegistry) namesLocked() []string {
	names := make([]string, 0, len(r.connectors))
	for n := range r.connectors {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
