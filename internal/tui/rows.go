package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type providerNode struct {
	ID, Name, BaseURL, Protocol, DefinitionID, Prefix, ModelsPath, AuthMode string
	Enabled                                                                 bool
}

type connection struct {
	ID, ProviderNodeID, Name, Email, CredentialType string
	Priority                                        int
	Enabled                                         bool
}

type catalogModel struct {
	ID, NodeID, Kind, ExternalID, DisplayName string
	Capabilities                              map[string]bool
}

type discoveredRoute struct {
	ID, ProviderNodeID, ProviderPrefix, Kind, ExternalID, DisplayName string
	Capabilities                                                      map[string]bool
	Enabled                                                           bool
	LastSeenAt                                                        string
}

type routeReference struct {
	RouteID string `json:"routeId"`
}
type modelReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type strategySpec struct {
	ID     string         `json:"id"`
	Config map[string]any `json:"config,omitempty"`
}

type physicalModel struct {
	Name         string           `json:"name"`
	Sources      []routeReference `json:"sources"`
	Policy       strategySpec     `json:"policy"`
	Capabilities map[string]bool  `json:"capabilities"`
	Discoverable bool             `json:"discoverable"`
	Enabled      bool             `json:"enabled"`
}

type comboModel struct {
	Name         string           `json:"name"`
	Members      []modelReference `json:"members"`
	Strategy     strategySpec     `json:"strategy"`
	Discoverable bool             `json:"discoverable"`
	Enabled      bool             `json:"enabled"`
}

type logicalModel struct{ Name, TargetRef string }

type combo struct {
	Name, Strategy string
	StickyLimit    int
	Members        []string
}

type publishedModel struct {
	Name, TargetRef, OwnedBy string
	Metadata                 map[string]any
}

type routeHealth struct {
	Failures      int
	Successes     int
	CooldownUntil time.Time
	LastError     string
	LastCause     string
	LastScope     string
	LastEvidence  string
	Confidence    float64
	LastAttempt   time.Time
	LastSuccess   time.Time
}

type quotaSnapshot struct {
	ProviderNodeID string
	ConnectionID   string
	ModelRef       string
	WindowName     string
	Used           float64
	Limit          *float64
	Remaining      *float64
	ResetAt        *time.Time
	Source         string
}

type usageRecord struct {
	ID                                                                           int64
	Timestamp, LogicalModel, ProviderNodeID, ExternalModel, ConnectionID, Status string
	LatencyMS, InputTokens, OutputTokens                                         int64
	EstimatedCost                                                                float64
	RequestClass, SessionID                                                      string
	TTFTMS                                                                       int64
	OutputTokensPerSecond                                                        float64
}

type entry struct {
	key, title, summary, detail, parentID string
	routeRef                              string
	modelRef                              string
	modelKind                             string
	publicName                            string
	kind                                  string
	exposed                               bool
	payload                               any
}

// modelWorkspace is the typed TUI-facing boundary over the daemon's separate
// discovered-route, physical-model and combo-model RPC contracts.
type modelWorkspace struct {
	Discovered []entry
	Physical   []entry
	Combos     []entry
}

type modelWorkspaceClient struct {
	providers []providerNode
}

func (c modelWorkspaceClient) Build(discoveredJSON, physicalJSON, comboJSON json.RawMessage) (modelWorkspace, error) {
	discovered, err := makeDiscoveredEntries(discoveredJSON, c.providers)
	if err != nil {
		return modelWorkspace{}, err
	}
	var physicalModels []physicalModel
	if err := json.Unmarshal(physicalJSON, &physicalModels); err != nil {
		return modelWorkspace{}, fmt.Errorf("decode physical models: %w", err)
	}
	var comboModels []comboModel
	if err := json.Unmarshal(comboJSON, &comboModels); err != nil {
		return modelWorkspace{}, fmt.Errorf("decode combo models: %w", err)
	}
	routes := make(map[string]discoveredRoute, len(discovered))
	for _, row := range discovered {
		if route, ok := row.payload.(discoveredRoute); ok {
			routes[route.ID] = route
		}
	}
	physical, combos := make([]entry, 0, len(physicalModels)), make([]entry, 0, len(comboModels))
	for _, value := range physicalModels {
		members := make([]string, 0, len(value.Sources))
		for _, source := range value.Sources {
			if route, ok := routes[source.RouteID]; ok {
				members = append(members, route.ProviderPrefix+"/"+route.ExternalID)
			} else {
				members = append(members, "route-id:"+source.RouteID)
			}
		}
		visibility := "hidden from /models"
		if value.Discoverable {
			visibility = "exposed in /models"
		}
		detail := fmt.Sprintf("Physical model\n\nName         %s\nSource policy %s\nCapabilities %s\nExposure     %s\nEnabled      %t\n\nOrdered discovered routes\n  %s", value.Name, value.Policy.ID, capabilitySummary(value.Capabilities), visibility, value.Enabled, strings.Join(members, "\n  ↓  "))
		physical = append(physical, entry{key: value.Name, title: value.Name, summary: fmt.Sprintf("%s · %d routes · %s", value.Policy.ID, len(value.Sources), visibility), detail: detail, modelRef: value.Name, modelKind: "physical", publicName: value.Name, exposed: value.Discoverable, payload: value})
	}
	for _, value := range comboModels {
		members := make([]string, 0, len(value.Members))
		for _, member := range value.Members {
			members = append(members, member.Kind+":"+member.ID)
		}
		visibility := "hidden from /models"
		if value.Discoverable {
			visibility = "exposed in /models"
		}
		detail := fmt.Sprintf("Combo model\n\nName       %s\nStrategy   %s\nExposure   %s\nEnabled    %t\n\nOrdered members\n  %s", value.Name, value.Strategy.ID, visibility, value.Enabled, strings.Join(members, "\n  ↓  "))
		combos = append(combos, entry{key: value.Name, title: value.Name, summary: fmt.Sprintf("%s · %d members · %s", value.Strategy.ID, len(value.Members), visibility), detail: detail, modelRef: value.Name, modelKind: "combo", publicName: value.Name, exposed: value.Discoverable, payload: value})
	}
	sort.Slice(physical, func(i, j int) bool { return physical[i].title < physical[j].title })
	sort.Slice(combos, func(i, j int) bool { return combos[i].title < combos[j].title })
	return modelWorkspace{Discovered: discovered, Physical: physical, Combos: combos}, nil
}

func capabilitySummary(value map[string]bool) string {
	var enabled []string
	for name, ok := range value {
		if ok {
			enabled = append(enabled, name)
		}
	}
	sort.Strings(enabled)
	if len(enabled) == 0 {
		return "not declared"
	}
	return strings.Join(enabled, ", ")
}

func (e entry) FilterValue() string {
	return strings.Join([]string{e.title, e.summary, e.detail, e.key, e.parentID}, " ")
}

func makeEntries(method string, raw json.RawMessage, knownProviders []providerNode) ([]entry, error) {
	decode := func(dst any) error { return json.Unmarshal(raw, dst) }
	switch method {
	case "status":
		var value map[string]any
		if err := decode(&value); err != nil {
			return nil, err
		}
		return []entry{{key: "daemon", title: "Daemon", summary: fmt.Sprintf("%v", value["status"]), detail: pretty(value), payload: value}}, nil
	case "providers.list":
		var values []providerNode
		if err := decode(&values); err != nil {
			return nil, err
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			state := "enabled"
			if !value.Enabled {
				state = "disabled"
			}
			detail := fmt.Sprintf("Provider\n\nName       %s\nPrefix     %s\nProtocol   %s\nEndpoint   %s\nModels     %s\nDefinition %s\nAuth       %s\nState      %s\n\nNode ID\n%s", value.Name, value.Prefix, value.Protocol, value.BaseURL, value.ModelsPath, value.DefinitionID, value.AuthMode, state, value.ID)
			entries = append(entries, entry{key: value.ID, title: value.Name, summary: fmt.Sprintf("%s  ·  %s  ·  %s", value.Prefix, value.Protocol, state), detail: detail, parentID: value.ID, payload: value})
		}
		return entries, nil
	case "connections.list":
		var values []connection
		if err := decode(&values); err != nil {
			return nil, err
		}
		providerNames := make(map[string]string, len(knownProviders))
		for _, node := range knownProviders {
			providerNames[node.ID] = node.Name
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			providerName := providerNames[value.ProviderNodeID]
			if providerName == "" {
				providerName = value.ProviderNodeID
			}
			state := "enabled"
			if !value.Enabled {
				state = "disabled"
			}
			detail := fmt.Sprintf("Connection\n\nName       %s\nProvider   %s\nCredential %s\nPriority   %d\nState      %s\nEmail      %s\n\nConnection ID\n%s\n\nProvider node ID\n%s\n\nSecret material is never shown here.", value.Name, providerName, value.CredentialType, value.Priority, state, value.Email, value.ID, value.ProviderNodeID)
			entries = append(entries, entry{key: value.ID, title: value.Name, summary: fmt.Sprintf("%s  ·  %s  ·  priority %d", providerName, value.CredentialType, value.Priority), detail: detail, parentID: value.ProviderNodeID, payload: value})
		}
		return entries, nil
	case "models.list":
		var values []catalogModel
		if err := decode(&values); err != nil {
			return nil, err
		}
		prefixByNode := make(map[string]string, len(knownProviders))
		nameByNode := make(map[string]string, len(knownProviders))
		for _, node := range knownProviders {
			prefixByNode[node.ID], nameByNode[node.ID] = node.Prefix, node.Name
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			physical := value.DisplayName
			if value.ExternalID != "" {
				physical = value.ExternalID
			}
			prefix := prefixByNode[value.NodeID]
			if prefix != "" {
				physical = prefix + "/" + strings.TrimPrefix(physical, prefix+"/")
			}
			nodeName := nameByNode[value.NodeID]
			capabilities := make([]string, 0, len(value.Capabilities))
			for capability, enabled := range value.Capabilities {
				if enabled {
					capabilities = append(capabilities, capability)
				}
			}
			sort.Strings(capabilities)
			caps := strings.Join(capabilities, ", ")
			if caps == "" {
				caps = "not declared"
			}
			detail := fmt.Sprintf("Physical model\n\nModel       %s\nProvider    %s\nKind        %s\nCapabilities %s\n\nCatalog ID\n%s\n\nProvider node ID\n%s\n\nUpstream model ID\n%s", physical, nodeName, value.Kind, caps, value.ID, value.NodeID, value.ExternalID)
			entries = append(entries, entry{key: value.ID, title: physical, summary: fmt.Sprintf("%s  ·  %s  ·  %s", nodeName, value.Kind, caps), detail: detail, parentID: value.NodeID, routeRef: physical, payload: value})
		}
		return entries, nil
	case "logical_models.list":
		var values []logicalModel
		if err := decode(&values); err != nil {
			return nil, err
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			detail := fmt.Sprintf("Logical model\n\nName   %s\nTarget %s", value.Name, value.TargetRef)
			entries = append(entries, entry{key: value.Name, title: value.Name, summary: value.TargetRef, detail: detail, payload: value})
		}
		return entries, nil
	case "combos.list":
		var values []combo
		if err := decode(&values); err != nil {
			return nil, err
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			members := strings.Join(value.Members, "\n  ↓  ")
			if members == "" {
				members = "(no members)"
			}
			detail := fmt.Sprintf("Combo\n\nName        %s\nStrategy    %s\nSticky limit %d\n\nOrdered members\n  %s", value.Name, value.Strategy, value.StickyLimit, members)
			entries = append(entries, entry{key: value.Name, title: value.Name, summary: fmt.Sprintf("%s  ·  %d members", value.Strategy, len(value.Members)), detail: detail, payload: value})
		}
		return entries, nil
	case "public_models.list":
		var values []publishedModel
		if err := decode(&values); err != nil {
			return nil, err
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			detail := fmt.Sprintf("Published model\n\nPublic name %s\nTarget      %s\nOwned by    %s", value.Name, value.TargetRef, value.OwnedBy)
			entries = append(entries, entry{key: value.Name, title: value.Name, summary: value.TargetRef, detail: detail, payload: value})
		}
		return entries, nil
	case "health.list":
		var values map[string]routeHealth
		if err := decode(&values); err != nil {
			return nil, err
		}
		entries := make([]entry, 0, len(values))
		for routeID, value := range values {
			state := "healthy"
			if !value.CooldownUntil.IsZero() {
				state = "cooldown until " + value.CooldownUntil.Format(time.RFC3339)
			}
			detail := fmt.Sprintf("Route health\n\nState      %s\nFailures   %d\nSuccesses  %d\nCause      %s\nScope      %s\nEvidence   %s\nConfidence %.2f\nLast error\n%s\n\nInternal route ID\n%s", state, value.Failures, value.Successes, value.LastCause, value.LastScope, value.LastEvidence, value.Confidence, value.LastError, routeID)
			entries = append(entries, entry{key: routeID, title: state, summary: fmt.Sprintf("%s · %d failures", value.LastCause, value.Failures), detail: detail, payload: value})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
		return entries, nil
	case "quota.list":
		var values []quotaSnapshot
		if err := decode(&values); err != nil {
			var byKey map[string]quotaSnapshot
			if mapErr := decode(&byKey); mapErr != nil {
				return nil, err
			}
			values = make([]quotaSnapshot, 0, len(byKey))
			for _, value := range byKey {
				values = append(values, value)
			}
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			name := value.ModelRef
			if name == "" {
				name = value.WindowName
			}
			remaining := "unknown"
			if value.Remaining != nil {
				remaining = fmt.Sprintf("%.0f remaining", *value.Remaining)
			}
			reset := "unknown"
			if value.ResetAt != nil {
				reset = value.ResetAt.Format(time.RFC3339)
			}
			detail := fmt.Sprintf("Quota\n\nModel      %s\nWindow     %s\nUsed       %.2f\nRemaining  %s\nReset      %s\nSource     %s\n\nProvider node ID\n%s\nConnection ID\n%s", value.ModelRef, value.WindowName, value.Used, remaining, reset, value.Source, value.ProviderNodeID, value.ConnectionID)
			entries = append(entries, entry{key: value.ProviderNodeID + value.ConnectionID + value.ModelRef + value.WindowName, title: name, summary: remaining, detail: detail, parentID: value.ProviderNodeID, payload: value})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
		return entries, nil
	case "usage.list":
		var values []usageRecord
		if err := decode(&values); err != nil {
			return nil, err
		}
		entries := make([]entry, 0, len(values))
		for _, value := range values {
			detail := fmt.Sprintf("Usage event\n\nModel      %s\nProvider   %s\nExternal   %s\nConnection %s\nStatus     %s\nClass      %s\nSession    %s\nLatency    %d ms\nTTFT       %d ms\nThroughput %.2f tok/s\nTokens     %d in / %d out\nCost       %.6f\nTimestamp  %s", value.LogicalModel, value.ProviderNodeID, value.ExternalModel, value.ConnectionID, value.Status, value.RequestClass, value.SessionID, value.LatencyMS, value.TTFTMS, value.OutputTokensPerSecond, value.InputTokens, value.OutputTokens, value.EstimatedCost, value.Timestamp)
			entries = append(entries, entry{key: fmt.Sprintf("%d", value.ID), title: value.LogicalModel, summary: fmt.Sprintf("%s · %d ms", value.Status, value.LatencyMS), detail: detail, payload: value})
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("unsupported TUI resource %q", method)
	}
}

func pretty(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func makeConnectionEntries(providerJSON, connectionJSON json.RawMessage, knownProviders []providerNode) ([]entry, error) {
	providers, err := makeEntries("providers.list", providerJSON, knownProviders)
	if err != nil {
		return nil, err
	}
	connections, err := makeEntries("connections.list", connectionJSON, knownProviders)
	if err != nil {
		return nil, err
	}
	for index := range providers {
		providers[index].kind = "provider"
		providers[index].title = "Provider · " + providers[index].title
	}
	for index := range connections {
		connections[index].kind = "connection"
		connections[index].title = "Key · " + connections[index].title
	}
	result := append(providers, connections...)
	return result, nil
}

func makeSourceEntries(catalogJSON json.RawMessage, knownProviders []providerNode) ([]entry, error) {
	items, err := makeEntries("models.list", catalogJSON, knownProviders)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].kind = "source"
	}
	return items, nil
}

func makeDiscoveredEntries(raw json.RawMessage, knownProviders []providerNode) ([]entry, error) {
	var values []discoveredRoute
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	providerNames := make(map[string]string, len(knownProviders))
	for _, provider := range knownProviders {
		providerNames[provider.ID] = provider.Name
	}
	items := make([]entry, 0, len(values))
	for _, value := range values {
		name := value.ProviderPrefix + "/" + value.ExternalID
		provider := providerNames[value.ProviderNodeID]
		if provider == "" {
			provider = value.ProviderPrefix
		}
		detail := fmt.Sprintf("Discovered provider route\n\nProvider     %s\nPrefix       %s\nUpstream ID  %s\nKind         %s\nEnabled      %t\nRoute ID     %s\nCapabilities %s", provider, value.ProviderPrefix, value.ExternalID, value.Kind, value.Enabled, value.ID, capabilitySummary(value.Capabilities))
		items = append(items, entry{key: value.ID, title: name, summary: fmt.Sprintf("%s · %s", provider, capabilitySummary(value.Capabilities)), detail: detail, parentID: value.ProviderNodeID, routeRef: name, modelRef: "", kind: "source", payload: value})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].title < items[j].title })
	return items, nil
}

func matchingPublicModel(public map[string]publishedModel, name, legacyKind string) (publishedModel, bool) {
	for _, value := range public {
		if value.TargetRef == name || value.TargetRef == legacyKind+":"+name {
			return value, true
		}
	}
	return publishedModel{}, false
}

func makeRuntimeEntries(healthJSON, quotaJSON json.RawMessage, providers []providerNode) ([]entry, error) {
	health, err := makeEntries("health.list", healthJSON, providers)
	if err != nil {
		return nil, err
	}
	quotaItems, err := makeEntries("quota.list", quotaJSON, providers)
	if err != nil {
		return nil, err
	}
	for index := range health {
		health[index].kind = "health"
		health[index].title = "Health · " + health[index].title
	}
	for index := range quotaItems {
		quotaItems[index].kind = "quota"
		quotaItems[index].title = "Quota · " + quotaItems[index].title
	}
	result := append(health, quotaItems...)
	return result, nil
}
