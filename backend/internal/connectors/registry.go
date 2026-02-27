package connectors

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
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

	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return nil, false
	}

	if connector, ok := r.connectors[trimmed]; ok {
		return connector, true
	}

	lower := strings.ToLower(trimmed)
	if connector, ok := r.connectors[lower]; ok {
		return connector, true
	}

	if normalized := normalizeConnectorKey(lower); normalized != "" {
		if connector, ok := r.connectors[normalized]; ok {
			return connector, true
		}
	}

	return nil, false
}

func normalizeConnectorKey(raw string) string {
	key := strings.TrimSpace(strings.ToLower(raw))
	if key == "" {
		return ""
	}

	if parsed, err := url.Parse(key); err == nil && parsed.Hostname() != "" {
		key = parsed.Hostname()
	} else {
		key = strings.TrimPrefix(key, "https://")
		key = strings.TrimPrefix(key, "http://")
		if slash := strings.IndexByte(key, '/'); slash >= 0 {
			key = key[:slash]
		}
		if question := strings.IndexByte(key, '?'); question >= 0 {
			key = key[:question]
		}
		if hash := strings.IndexByte(key, '#'); hash >= 0 {
			key = key[:hash]
		}
		if colon := strings.IndexByte(key, ':'); colon >= 0 {
			key = key[:colon]
		}
	}

	key = strings.TrimPrefix(key, "www.")

	switch key {
	case "mangadex.org":
		return "mangadex"
	case "mangafire.to":
		return "mangafire"
	case "asuracomic.net":
		return "asuracomic"
	case "flamecomics.xyz":
		return "flamecomics"
	case "mgeko.cc":
		return "mgeko"
	case "webtoons.com", "m.webtoons.com":
		return "webtoons"
	default:
		return key
	}
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

	statuses := make([]HealthStatus, len(list))
	var wg sync.WaitGroup
	wg.Add(len(list))
	for index, connector := range list {
		index := index
		connector := connector
		go func() {
			defer wg.Done()

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
			statuses[index] = status
		}()
	}
	wg.Wait()

	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Key < statuses[j].Key
	})

	return statuses
}
