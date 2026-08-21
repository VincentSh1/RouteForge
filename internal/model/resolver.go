package model

import "strings"

const (
	LogicalPrefix = "routeforge/"
	General       = "routeforge/general"
)

type ErrorKind string

const (
	ErrorUnknownAlias   ErrorKind = "unknown_alias"
	ErrorMissingMapping ErrorKind = "missing_mapping"
)

type ResolutionError struct {
	Kind ErrorKind
}

func (e *ResolutionError) Error() string {
	if e.Kind == ErrorUnknownAlias {
		return "unknown logical model"
	}
	return "logical model is unavailable for the selected provider"
}

type Resolver struct {
	mappings map[string]map[string]string
}

func New(mappings map[string]map[string]string) *Resolver {
	resolver := &Resolver{mappings: make(map[string]map[string]string, len(mappings))}
	for alias, providerMappings := range mappings {
		resolver.mappings[alias] = make(map[string]string, len(providerMappings))
		for providerName, providerModel := range providerMappings {
			resolver.mappings[alias][providerName] = providerModel
		}
	}
	return resolver
}

func IsLogical(name string) bool {
	return strings.HasPrefix(name, LogicalPrefix)
}

func (r *Resolver) Resolve(logicalModel, providerName string) (string, error) {
	providerMappings, ok := r.mappings[logicalModel]
	if !ok {
		return "", &ResolutionError{Kind: ErrorUnknownAlias}
	}
	providerModel := providerMappings[providerName]
	if strings.TrimSpace(providerModel) == "" {
		return "", &ResolutionError{Kind: ErrorMissingMapping}
	}
	return providerModel, nil
}
