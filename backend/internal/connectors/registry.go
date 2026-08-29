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

// Get resolves a connector from a key, which callers spell loosely: the
// registered key in any casing, a bare hostname, or a full URL. Host-shaped
// keys are matched against the Hosts() each registered connector publishes
// through SiteInfo, so a new connector's domains map without touching the
// registry.
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

	if host := hostFromKey(lower); host != "" {
		if connector, ok := r.findByHostLocked(host); ok {
			return connector, true
		}
		// A key spelled as a URL of the connector's own key ("https://mykey/")
		// still resolves even without SiteInfo metadata.
		if connector, ok := r.connectors[host]; ok {
			return connector, true
		}
	}

	return nil, false
}

// GetByURL resolves the connector responsible for rawURL via the Hosts() the
// registered connectors publish.
func (r *Registry) GetByURL(rawURL string) (Connector, bool) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Hostname() == "" {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.findByHostLocked(strings.ToLower(parsed.Hostname()))
}

// findByHostLocked scans the registered connectors' SiteInfo host claims.
// Callers hold r.mu.
func (r *Registry) findByHostLocked(host string) (Connector, bool) {
	for _, connector := range r.connectors {
		info, ok := connector.(SiteInfo)
		if !ok {
			continue
		}
		if HostAllowed(host, info.Hosts()) {
			return connector, true
		}
	}
	return nil, false
}

// hostFromKey extracts a hostname from a loosely spelled key: a full URL, a
// scheme-less "host/path" string, or a bare host, with any leading "www."
// stripped (subdomain matching in HostAllowed covers it anyway).
func hostFromKey(raw string) string {
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

	return strings.TrimPrefix(key, "www.")
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
