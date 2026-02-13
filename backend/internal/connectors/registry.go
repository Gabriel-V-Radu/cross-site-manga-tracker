package connectors

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type Registry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
}

type Descriptor struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type HealthStatus struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

func NewRegistry() *Registry {
	return &Registry{connectors: map[string]Connector{}}
}

func (r *Registry) Register(connector Connector) error {
	if connector == nil {
		return fmt.Errorf("connector is nil")
	}

	key := connector.Key()
	if key == "" {
		return fmt.Errorf("connector key is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.connectors[key]; exists {
		return fmt.Errorf("connector %q already registered", key)
	}

	r.connectors[key] = connector
	return nil
}

func (r *Registry) Get(key string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	connector, ok := r.connectors[key]
	return connector, ok
}

func (r *Registry) List() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]Descriptor, 0, len(r.connectors))
	for _, connector := range r.connectors {
		items = append(items, Descriptor{
			Key:  connector.Key(),
			Name: connector.Name(),
			Kind: connector.Kind(),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})

	return items
}

func (r *Registry) Health(ctx context.Context) []HealthStatus {
	r.mu.RLock()
	list := make([]Connector, 0, len(r.connectors))
	for _, connector := range r.connectors {
		list = append(list, connector)
	}
	r.mu.RUnlock()

	statuses := make([]HealthStatus, 0, len(list))
	for _, connector := range list {
		err := connector.HealthCheck(ctx)
		status := HealthStatus{
			Key:     connector.Key(),
			Name:    connector.Name(),
			Kind:    connector.Kind(),
			Healthy: err == nil,
		}
		if err != nil {
			status.Error = err.Error()
		}
		statuses = append(statuses, status)
	}

	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Key < statuses[j].Key
	})

	return statuses
}
