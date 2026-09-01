// Package resourcecatalog loads the administrator-owned SLS resource and ACL catalog.
package resourcecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

var (
	fieldNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	projectPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	logStorePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,61}[a-z0-9]$`)
)

type catalogFile struct {
	Version   string           `json:"version"`
	Resources []resourceConfig `json:"resources"`
	Bindings  []bindingConfig  `json:"bindings"`
}

type resourceConfig struct {
	ID              string               `json:"id"`
	Service         string               `json:"service"`
	Environment     string               `json:"environment"`
	Endpoint        string               `json:"endpoint"`
	Project         string               `json:"project"`
	LogStore        string               `json:"logstore"`
	TemplateVersion string               `json:"template_version"`
	Selectors       []domain.LogSelector `json:"selectors"`
	ErrorSelector   domain.LogSelector   `json:"error_selector"`
	ErrorField      string               `json:"error_field"`
	InstanceField   string               `json:"instance_field"`
}

type bindingConfig struct {
	Principal   domain.Principal `json:"principal"`
	ResourceIDs []string         `json:"resource_ids"`
}

type logicalScope struct {
	service     string
	environment string
}

// Catalog is an immutable resource index and default-deny ACL after loading.
type Catalog struct {
	resourcesByScope map[logicalScope]domain.LogResource
	resourcesByID    map[string]domain.LogResource
	resources        []domain.LogResource
	allowed          map[domain.Principal]map[string]struct{}
}

// Load reads and validates a versioned JSON catalog from path.
func Load(path string) (*Catalog, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("resource catalog path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open resource catalog: %w", err)
	}
	defer file.Close()

	config, err := decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode resource catalog: %w", err)
	}
	catalog, err := build(config)
	if err != nil {
		return nil, fmt.Errorf("validate resource catalog: %w", err)
	}
	return catalog, nil
}

func decode(reader io.Reader) (catalogFile, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var config catalogFile
	if err := decoder.Decode(&config); err != nil {
		return catalogFile{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return catalogFile{}, errors.New("catalog must contain exactly one JSON value")
		}
		return catalogFile{}, fmt.Errorf("read trailing JSON: %w", err)
	}
	return config, nil
}

func build(config catalogFile) (*Catalog, error) {
	if err := requiredValue("version", config.Version); err != nil {
		return nil, err
	}
	if len(config.Resources) == 0 {
		return nil, errors.New("at least one resource is required")
	}

	catalog := &Catalog{
		resourcesByScope: make(map[logicalScope]domain.LogResource, len(config.Resources)),
		resourcesByID:    make(map[string]domain.LogResource, len(config.Resources)),
		resources:        make([]domain.LogResource, 0, len(config.Resources)),
		allowed:          make(map[domain.Principal]map[string]struct{}, len(config.Bindings)),
	}
	for index, item := range config.Resources {
		resource, err := validateResource(config.Version, item)
		if err != nil {
			return nil, fmt.Errorf("resource %d: %w", index, err)
		}
		if _, duplicate := catalog.resourcesByID[resource.ID]; duplicate {
			return nil, fmt.Errorf("duplicate resource ID %q", resource.ID)
		}
		key := scopeKey(resource.Service, resource.Environment)
		if existing, duplicate := catalog.resourcesByScope[key]; duplicate {
			return nil, fmt.Errorf(
				"duplicate service/environment %q/%q for resources %q and %q",
				resource.Service, resource.Environment, existing.ID, resource.ID,
			)
		}
		catalog.resourcesByID[resource.ID] = resource
		catalog.resourcesByScope[key] = resource
		catalog.resources = append(catalog.resources, resource)
	}
	if err := catalog.addBindings(config.Bindings); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validateResource(version string, item resourceConfig) (domain.LogResource, error) {
	values := []struct {
		name  string
		value string
	}{
		{name: "id", value: item.ID},
		{name: "service", value: item.Service},
		{name: "environment", value: item.Environment},
		{name: "project", value: item.Project},
		{name: "logstore", value: item.LogStore},
		{name: "template_version", value: item.TemplateVersion},
	}
	for _, value := range values {
		if err := requiredValue(value.name, value.value); err != nil {
			return domain.LogResource{}, err
		}
	}
	if err := domain.ValidateResourceID(item.ID); err != nil {
		return domain.LogResource{}, err
	}
	contract, knownTemplate := domain.QueryTemplateByVersion(item.TemplateVersion)
	if !knownTemplate {
		return domain.LogResource{}, fmt.Errorf("unsupported template_version %q", item.TemplateVersion)
	}
	if err := validateEndpoint(item.Endpoint); err != nil {
		return domain.LogResource{}, err
	}
	if !projectPattern.MatchString(item.Project) {
		return domain.LogResource{}, errors.New("project does not match the SLS naming rules")
	}
	if !logStorePattern.MatchString(item.LogStore) {
		return domain.LogResource{}, errors.New("logstore does not match the SLS naming rules")
	}
	if contract.Dimensional {
		if err := validateFieldName("error_field", item.ErrorField); err != nil {
			return domain.LogResource{}, err
		}
		if err := validateFieldName("instance_field", item.InstanceField); err != nil {
			return domain.LogResource{}, err
		}
		if item.InstanceField == item.ErrorField {
			return domain.LogResource{}, errors.New("instance_field must differ from error_field")
		}
	} else if item.ErrorField != "" || item.InstanceField != "" {
		return domain.LogResource{}, errors.New("count-only template must not configure error_field or instance_field")
	}
	if len(item.Selectors) == 0 {
		return domain.LogResource{}, errors.New("at least one fixed selector is required")
	}
	if err := validateSelector("error_selector", item.ErrorSelector); err != nil {
		return domain.LogResource{}, err
	}

	selectors := make([]domain.LogSelector, len(item.Selectors))
	seenFields := make(map[string]struct{}, len(item.Selectors))
	for index, selector := range item.Selectors {
		if err := validateSelector(fmt.Sprintf("selectors[%d]", index), selector); err != nil {
			return domain.LogResource{}, err
		}
		if _, duplicate := seenFields[selector.Field]; duplicate {
			return domain.LogResource{}, fmt.Errorf("duplicate selector field %q", selector.Field)
		}
		seenFields[selector.Field] = struct{}{}
		selectors[index] = selector
	}
	if _, duplicate := seenFields[item.ErrorSelector.Field]; duplicate {
		return domain.LogResource{}, fmt.Errorf("error_selector duplicates selector field %q", item.ErrorSelector.Field)
	}
	if contract.Dimensional {
		if _, duplicate := seenFields[item.ErrorField]; duplicate {
			return domain.LogResource{}, fmt.Errorf("error_field duplicates selector field %q", item.ErrorField)
		}
		if item.ErrorField == item.ErrorSelector.Field {
			return domain.LogResource{}, fmt.Errorf("error_field duplicates error_selector field %q", item.ErrorField)
		}
		if _, duplicate := seenFields[item.InstanceField]; duplicate {
			return domain.LogResource{}, fmt.Errorf("instance_field duplicates selector field %q", item.InstanceField)
		}
		if item.InstanceField == item.ErrorSelector.Field {
			return domain.LogResource{}, fmt.Errorf("instance_field duplicates error_selector field %q", item.InstanceField)
		}
	}

	return domain.LogResource{
		ID:              item.ID,
		CatalogVersion:  version,
		Service:         item.Service,
		Environment:     item.Environment,
		Endpoint:        item.Endpoint,
		Project:         item.Project,
		LogStore:        item.LogStore,
		TemplateVersion: item.TemplateVersion,
		Selectors:       selectors,
		ErrorSelector:   item.ErrorSelector,
		ErrorField:      item.ErrorField,
		InstanceField:   item.InstanceField,
	}, nil
}

func validateEndpoint(raw string) error {
	if err := requiredValue("endpoint", raw); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("endpoint %q must be an absolute HTTPS URL", raw)
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("endpoint must contain only an HTTPS scheme and host")
	}
	return nil
}

func validateFieldName(name, value string) error {
	if !fieldNamePattern.MatchString(value) {
		return fmt.Errorf("%s %q must match %s", name, value, fieldNamePattern.String())
	}
	return nil
}

func validateSelector(name string, selector domain.LogSelector) error {
	if err := validateFieldName(name+".field", selector.Field); err != nil {
		return err
	}
	if strings.TrimSpace(selector.Value) == "" {
		return fmt.Errorf("%s.value is required", name)
	}
	if len(selector.Value) > 256 {
		return fmt.Errorf("%s.value exceeds 256 bytes", name)
	}
	if strings.IndexFunc(selector.Value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s.value contains control characters", name)
	}
	return nil
}

func requiredValue(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be non-empty and have no surrounding whitespace", name)
	}
	return nil
}

func (c *Catalog) addBindings(bindings []bindingConfig) error {
	for index, binding := range bindings {
		if !binding.Principal.Complete() {
			return fmt.Errorf("binding %d principal must include app_id, tenant_key, and user_id", index)
		}
		for _, value := range []struct {
			name  string
			value string
		}{
			{name: "app_id", value: binding.Principal.AppID},
			{name: "tenant_key", value: binding.Principal.TenantKey},
			{name: "user_id", value: binding.Principal.UserID},
		} {
			if err := requiredValue(value.name, value.value); err != nil {
				return fmt.Errorf("binding %d: %w", index, err)
			}
		}
		if _, duplicate := c.allowed[binding.Principal]; duplicate {
			return fmt.Errorf("duplicate binding for principal %q", binding.Principal.Key())
		}
		if len(binding.ResourceIDs) == 0 {
			return fmt.Errorf("binding %d must include at least one resource_id", index)
		}

		resources := make(map[string]struct{}, len(binding.ResourceIDs))
		for _, resourceID := range binding.ResourceIDs {
			if _, exists := c.resourcesByID[resourceID]; !exists {
				return fmt.Errorf("binding %d references unknown resource %q", index, resourceID)
			}
			if _, duplicate := resources[resourceID]; duplicate {
				return fmt.Errorf("binding %d contains duplicate resource %q", index, resourceID)
			}
			resources[resourceID] = struct{}{}
		}
		c.allowed[binding.Principal] = resources
	}
	return nil
}

// Resolve returns the single administrator-owned resource for a logical scope.
func (c *Catalog) Resolve(ctx context.Context, service, environment string) (domain.LogResource, error) {
	if err := ctx.Err(); err != nil {
		return domain.LogResource{}, err
	}
	resource, exists := c.resourcesByScope[scopeKey(service, environment)]
	if !exists {
		return domain.LogResource{}, fmt.Errorf("resolve log resource %q/%q: %w", service, environment, ports.ErrNotFound)
	}
	return cloneResource(resource), nil
}

// Allowed applies an exact, full-principal binding. Missing data always denies access.
func (c *Catalog) Allowed(ctx context.Context, principal domain.Principal, resourceID string) bool {
	if ctx.Err() != nil || !principal.Complete() || resourceID == "" {
		return false
	}
	resources, exists := c.allowed[principal]
	if !exists {
		return false
	}
	_, allowed := resources[resourceID]
	return allowed
}

// Resources returns a defensive copy in the order declared by the catalog.
func (c *Catalog) Resources() []domain.LogResource {
	resources := make([]domain.LogResource, len(c.resources))
	for index, resource := range c.resources {
		resources[index] = cloneResource(resource)
	}
	return resources
}

func scopeKey(service, environment string) logicalScope {
	return logicalScope{service: service, environment: environment}
}

func cloneResource(resource domain.LogResource) domain.LogResource {
	resource.Selectors = append([]domain.LogSelector(nil), resource.Selectors...)
	return resource
}

var _ ports.ResourceCatalog = (*Catalog)(nil)
