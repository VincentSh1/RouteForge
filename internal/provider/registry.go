package provider

type Registry struct {
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	registry := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, item := range providers {
		registry.providers[item.Name()] = item
	}
	return registry
}

func (r *Registry) Get(name string) (Provider, bool) {
	item, ok := r.providers[name]
	return item, ok
}
