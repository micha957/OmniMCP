package core

import (
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"sync"
)

type Registry struct {
	mu      sync.RWMutex
	plugins map[string]plugin.Plugin
	tools   map[string]toolRef
	enabled map[string]bool
}
type toolRef struct {
	p plugin.Plugin
	t plugin.Tool
}

func NewRegistry() *Registry {
	return &Registry{plugins: map[string]plugin.Plugin{}, tools: map[string]toolRef{}, enabled: map[string]bool{}}
}
func (r *Registry) Register(p plugin.Plugin) error {
	m := p.Manifest()
	if m.ID == "" {
		return fmt.Errorf("plugin id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.plugins[m.ID]; ok {
		return fmt.Errorf("plugin %q already registered", m.ID)
	}
	r.plugins[m.ID] = p
	r.enabled[m.ID] = true
	added := make([]string, 0, len(p.Tools()))
	for _, t := range p.Tools() {
		key := m.ID + "_" + t.Name
		if _, ok := r.tools[key]; ok {
			delete(r.plugins, m.ID)
			delete(r.enabled, m.ID)
			for _, k := range added {
				delete(r.tools, k)
			}
			return fmt.Errorf("tool %q already registered", key)
		}
		r.tools[key] = toolRef{p, t}
		added = append(added, key)
	}
	return nil
}
func (r *Registry) Tools() map[string]toolRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]toolRef{}
	for k, v := range r.tools {
		if r.enabled[v.p.Manifest().ID] {
			out[k] = v
		}
	}
	return out
}

func (r *Registry) SetEnabled(id string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.plugins[id]; !ok {
		return fmt.Errorf("plugin %q is not registered", id)
	}
	r.enabled[id] = enabled
	return nil
}
func (r *Registry) Enabled(id string) bool { r.mu.RLock(); defer r.mu.RUnlock(); return r.enabled[id] }
func (r *Registry) Plugins() []plugin.Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]plugin.Manifest, 0, len(r.plugins))
	for id, p := range r.plugins {
		m := p.Manifest()
		m.ID = id
		out = append(out, m)
	}
	return out
}
func (r *Registry) Lookup(name string) (toolRef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.tools[name]
	return v, ok
}
