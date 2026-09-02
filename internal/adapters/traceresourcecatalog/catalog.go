// Package traceresourcecatalog loads administrator-owned Trace resource groups.
package traceresourcecatalog

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

	"logagent/internal/domain"
	"logagent/internal/ports"
)

var (
	traceFieldPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	traceProjectPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	traceLogStorePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,61}[a-z0-9]$`)
	logicalValuePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type fileConfig struct {
	Version  string                      `json:"version"`
	Groups   []domain.TraceResourceGroup `json:"groups"`
	Bindings []bindingConfig             `json:"bindings"`
}

type bindingConfig struct {
	Principal domain.Principal `json:"principal"`
	GroupIDs  []string         `json:"group_ids"`
}

type scope struct {
	service     string
	environment string
}

type Catalog struct {
	groupsByScope map[scope]domain.TraceResourceGroup
	groupsByID    map[string]domain.TraceResourceGroup
	groups        []domain.TraceResourceGroup
	allowed       map[domain.Principal]map[string]struct{}
}

func Load(path string) (*Catalog, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("Trace resource catalog path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Trace resource catalog: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config fileConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode Trace resource catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("Trace resource catalog must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("read trailing Trace catalog JSON: %w", err)
	}
	if strings.TrimSpace(config.Version) == "" || strings.TrimSpace(config.Version) != config.Version {
		return nil, errors.New("Trace resource catalog version is required without surrounding whitespace")
	}
	for index := range config.Groups {
		if config.Groups[index].CatalogVersion != "" {
			return nil, fmt.Errorf("group %d must not provide catalog_version", index)
		}
		config.Groups[index].CatalogVersion = config.Version
	}
	return New(config.Groups, bindingsMap(config.Bindings))
}

// New validates an immutable in-memory catalog. It is also used by the
// deterministic Mock assembly without introducing a second policy path.
func New(groups []domain.TraceResourceGroup, bindings map[domain.Principal][]string) (*Catalog, error) {
	if len(groups) == 0 {
		return nil, errors.New("at least one Trace resource group is required")
	}
	catalog := &Catalog{
		groupsByScope: make(map[scope]domain.TraceResourceGroup, len(groups)),
		groupsByID:    make(map[string]domain.TraceResourceGroup, len(groups)),
		groups:        make([]domain.TraceResourceGroup, 0, len(groups)),
		allowed:       make(map[domain.Principal]map[string]struct{}, len(bindings)),
	}
	for index, group := range groups {
		validated, err := validateGroup(group)
		if err != nil {
			return nil, fmt.Errorf("Trace group %d: %w", index, err)
		}
		if _, exists := catalog.groupsByID[validated.ID]; exists {
			return nil, fmt.Errorf("duplicate Trace group ID %q", validated.ID)
		}
		key := scope{service: validated.Service, environment: validated.Environment}
		if _, exists := catalog.groupsByScope[key]; exists {
			return nil, fmt.Errorf("duplicate Trace scope %q/%q", validated.Service, validated.Environment)
		}
		catalog.groupsByID[validated.ID] = validated
		catalog.groupsByScope[key] = validated
		catalog.groups = append(catalog.groups, validated)
	}
	for principal, groupIDs := range bindings {
		if !principal.Complete() || len(groupIDs) == 0 {
			return nil, errors.New("Trace binding requires a complete principal and at least one group")
		}
		allowed := make(map[string]struct{}, len(groupIDs))
		for _, groupID := range groupIDs {
			if _, exists := catalog.groupsByID[groupID]; !exists {
				return nil, fmt.Errorf("Trace binding references unknown group %q", groupID)
			}
			if _, duplicate := allowed[groupID]; duplicate {
				return nil, fmt.Errorf("Trace binding contains duplicate group %q", groupID)
			}
			allowed[groupID] = struct{}{}
		}
		catalog.allowed[principal] = allowed
	}
	return catalog, nil
}

func bindingsMap(bindings []bindingConfig) map[domain.Principal][]string {
	result := make(map[domain.Principal][]string, len(bindings))
	for _, binding := range bindings {
		// Duplicate principals fail in New by preserving an invalid duplicate ID.
		if _, exists := result[binding.Principal]; exists {
			result[binding.Principal] = append(result[binding.Principal], "__duplicate_principal__")
			continue
		}
		result[binding.Principal] = append([]string(nil), binding.GroupIDs...)
	}
	return result
}

func validateGroup(group domain.TraceResourceGroup) (domain.TraceResourceGroup, error) {
	if err := domain.ValidateResourceID(group.ID); err != nil {
		return domain.TraceResourceGroup{}, err
	}
	if strings.TrimSpace(group.CatalogVersion) == "" || strings.TrimSpace(group.CatalogVersion) != group.CatalogVersion {
		return domain.TraceResourceGroup{}, errors.New("catalog_version is required")
	}
	if !logicalValuePattern.MatchString(group.Service) || !logicalValuePattern.MatchString(group.Environment) {
		return domain.TraceResourceGroup{}, errors.New("service and environment must use safe logical identifiers")
	}
	if group.TemplateVersion != domain.TraceSearchTemplateVersion {
		return domain.TraceResourceGroup{}, fmt.Errorf("unsupported Trace template version %q", group.TemplateVersion)
	}
	if len(group.Members) == 0 || len(group.Members) > domain.TraceMaximumMembers {
		return domain.TraceResourceGroup{}, fmt.Errorf("Trace group must contain 1-%d members", domain.TraceMaximumMembers)
	}
	seen := make(map[string]struct{}, len(group.Members))
	primaryFound := false
	members := make([]domain.TraceResourceMember, len(group.Members))
	for index, member := range group.Members {
		validated, err := validateMember(member)
		if err != nil {
			return domain.TraceResourceGroup{}, fmt.Errorf("member %d: %w", index, err)
		}
		if _, duplicate := seen[validated.ID]; duplicate {
			return domain.TraceResourceGroup{}, fmt.Errorf("duplicate member ID %q", validated.ID)
		}
		seen[validated.ID] = struct{}{}
		primaryFound = primaryFound || validated.ID == group.PrimaryMemberID
		members[index] = validated
	}
	if !primaryFound {
		return domain.TraceResourceGroup{}, errors.New("primary_member_id must identify exactly one group member")
	}
	group.Members = members
	return group, nil
}

func validateMember(member domain.TraceResourceMember) (domain.TraceResourceMember, error) {
	if err := domain.ValidateResourceID(member.ID); err != nil {
		return domain.TraceResourceMember{}, err
	}
	parsed, err := url.Parse(member.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return domain.TraceResourceMember{}, errors.New("endpoint must contain only an HTTPS scheme and host")
	}
	if !traceProjectPattern.MatchString(member.Project) || !traceLogStorePattern.MatchString(member.LogStore) {
		return domain.TraceResourceMember{}, errors.New("project or Logstore does not match SLS naming rules")
	}
	if member.TraceMode != domain.TraceQueryField && member.TraceMode != domain.TraceQueryFullText {
		return domain.TraceResourceMember{}, errors.New("trace_mode must be FIELD or FULLTEXT")
	}
	if member.TraceMode == domain.TraceQueryField {
		if !traceFieldPattern.MatchString(member.TraceField) {
			return domain.TraceResourceMember{}, errors.New("FIELD trace mode requires a safe trace_field")
		}
	} else if member.TraceField != "" {
		return domain.TraceResourceMember{}, errors.New("FULLTEXT trace mode must not configure trace_field")
	}
	if member.EnvironmentMode != domain.TraceEnvironmentField && member.EnvironmentMode != domain.TraceEnvironmentFullText {
		return domain.TraceResourceMember{}, errors.New("environment_mode must be FIELD or FULLTEXT")
	}
	if member.EnvironmentMode == domain.TraceEnvironmentField {
		if !traceFieldPattern.MatchString(member.EnvironmentField) {
			return domain.TraceResourceMember{}, errors.New("FIELD environment mode requires a safe environment_field")
		}
	} else if member.EnvironmentField != "" {
		return domain.TraceResourceMember{}, errors.New("FULLTEXT environment mode must not configure environment_field")
	}
	for name, field := range map[string]string{
		"message_field": member.MessageField, "event_time_field": member.EventTimeField,
	} {
		if !traceFieldPattern.MatchString(field) {
			return domain.TraceResourceMember{}, fmt.Errorf("%s must be a safe field", name)
		}
	}
	for name, field := range map[string]string{
		"level_field": member.LevelField, "operation_field": member.OperationField,
		"receive_time_field": member.ReceiveTimeField, "nanosecond_time_field": member.NanosecondTimeField,
	} {
		if field != "" && !traceFieldPattern.MatchString(field) {
			return domain.TraceResourceMember{}, fmt.Errorf("%s must be empty or a safe field", name)
		}
	}
	return member, nil
}

func (c *Catalog) ResolveTraceGroup(ctx context.Context, service, environment string) (domain.TraceResourceGroup, error) {
	if err := ctx.Err(); err != nil {
		return domain.TraceResourceGroup{}, err
	}
	group, exists := c.groupsByScope[scope{service: service, environment: environment}]
	if !exists {
		return domain.TraceResourceGroup{}, fmt.Errorf("resolve Trace group %q/%q: %w", service, environment, ports.ErrNotFound)
	}
	return cloneGroup(group), nil
}

func (c *Catalog) AllowedTraceGroup(ctx context.Context, principal domain.Principal, groupID string) bool {
	if ctx.Err() != nil || !principal.Complete() || groupID == "" {
		return false
	}
	groups, exists := c.allowed[principal]
	if !exists {
		return false
	}
	_, exists = groups[groupID]
	return exists
}

func (c *Catalog) ListAllowedCapabilities(ctx context.Context, principal domain.Principal) ([]domain.InvestigationCapability, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !principal.Complete() {
		return nil, ports.ErrIntentForbidden
	}
	allowed := c.allowed[principal]
	result := make([]domain.InvestigationCapability, 0, len(allowed))
	for _, group := range c.groups {
		if _, exists := allowed[group.ID]; !exists {
			continue
		}
		result = append(result, domain.InvestigationCapability{
			Service: group.Service, Environment: group.Environment,
			Intent: domain.IntentTraceSearch, TemplateID: domain.TraceSearchTemplateID,
		})
	}
	return result, nil
}

func (c *Catalog) Groups() []domain.TraceResourceGroup {
	result := make([]domain.TraceResourceGroup, len(c.groups))
	for index, group := range c.groups {
		result[index] = cloneGroup(group)
	}
	return result
}

func cloneGroup(group domain.TraceResourceGroup) domain.TraceResourceGroup {
	group.Members = append([]domain.TraceResourceMember(nil), group.Members...)
	return group
}

var _ ports.TraceResourceCatalog = (*Catalog)(nil)
