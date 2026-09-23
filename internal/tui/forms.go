package tui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type fieldSpec struct {
	key, label, placeholder string
	secret                  bool
}

type formState struct {
	title, method string
	fields        []fieldSpec
	inputs        []textinput.Model
	active        int
	comboMembers  []string
	typedSources  []routeReference
	typedMembers  []modelReference
	memberInput   textinput.Model
	memberMode    bool
	addingMember  bool
	memberCursor  int
	extra         map[string]any
}

func buildForm(title, method string, specs []fieldSpec, values map[string]string) *formState {
	f := &formState{title: title, method: method, fields: specs, extra: map[string]any{}}
	f.inputs = make([]textinput.Model, len(specs))
	for i, spec := range specs {
		input := textinput.New()
		input.Prompt = ""
		input.Placeholder = spec.placeholder
		input.SetWidth(64)
		input.SetValue(values[spec.key])
		input.CharLimit = 512
		if spec.secret {
			input.EchoMode = textinput.EchoPassword
			input.EchoCharacter = '•'
		}
		if i == 0 {
			input.Focus()
		} else {
			input.Blur()
		}
		f.inputs[i] = input
	}
	f.memberInput = textinput.New()
	f.memberInput.Prompt = ""
	f.memberInput.Placeholder = "physical route / logical model / combo"
	f.memberInput.CharLimit = 512
	f.memberInput.SetWidth(64)
	f.memberInput.Blur()
	return f
}

func (f *formState) SetWidth(width int) {
	for index := range f.inputs {
		f.inputs[index].SetWidth(width)
	}
	f.memberInput.SetWidth(width)
}

func newResourceForm(section int, selected *entry, providerID string) *formState {
	values := map[string]string{}
	if selected != nil {
		switch value := selected.payload.(type) {
		case providerNode:
			values = map[string]string{"name": value.Name, "prefix": value.Prefix, "baseURL": value.BaseURL, "protocol": value.Protocol, "definitionID": value.DefinitionID, "modelsPath": value.ModelsPath, "authMode": value.AuthMode}
			f := buildForm("Edit provider", "providers.update", providerFields(), values)
			f.extra["id"] = value.ID
			return f
		case connection:
			values = map[string]string{"name": value.Name, "credentialType": value.CredentialType, "priority": strconv.Itoa(value.Priority)}
			f := buildForm("Edit connection", "connections.update", connectionEditFields(), values)
			f.extra["id"] = value.ID
			return f
		case catalogModel:
			if value.Kind != "custom" {
				return nil
			}
			values = map[string]string{"providerNodeID": value.NodeID, "kind": value.Kind, "externalID": value.ExternalID, "displayName": value.DisplayName}
			f := buildForm("Edit physical model", "custom_models.upsert", modelEditFields(), values)
			f.extra["id"] = value.ID
			f.extra["profile"] = value.Profile
			return f
		case physicalModel:
			f := buildForm("Edit physical model", "physical_models.upsert", physicalModelEditFields(), map[string]string{"policy": value.Policy.ID, "discoverable": strconv.FormatBool(value.Discoverable)})
			f.extra["name"] = value.Name
			f.extra["profile"] = value.Profile
			f.extra["identity"] = value.Identity
			f.extra["limits"] = value.Limits
			f.extra["enabled"] = value.Enabled
			f.typedSources = append([]routeReference(nil), value.Sources...)
			return f
		case comboModel:
			f := buildForm("Edit combo model", "combo_models.upsert", comboModelEditFields(), map[string]string{"strategy": value.Strategy.ID, "discoverable": strconv.FormatBool(value.Discoverable)})
			f.extra["name"] = value.Name
			f.extra["enabled"] = value.Enabled
			f.typedMembers = append([]modelReference(nil), value.Members...)
			return f
		}
	}

	switch sectionID(section) {
	case sectionProviders:
		values = map[string]string{"protocol": "openai_chat", "definitionID": "openai-compatible-chat", "modelsPath": "/models", "authMode": "api_key"}
		return buildForm("Add provider", "providers.create", providerFields(), values)
	case sectionConnections:
		values = map[string]string{"providerNodeID": providerID, "credentialType": "api_key", "priority": "100"}
		return buildForm("Add connection", "connections.create", connectionFields(), values)
	case sectionModels:
		return buildForm("Add physical model", "custom_models.upsert", modelFields(), map[string]string{"kind": "custom", "providerNodeID": providerID})
	case sectionPhysical:
		return buildForm("Create physical model", "physical_models.upsert", physicalModelFields(), map[string]string{"policy": "ordered-fallback", "discoverable": "false"})
	case sectionComboModels:
		return buildForm("Create combo model", "combo_models.upsert", comboModelFields(), map[string]string{"strategy": "ordered-fallback", "discoverable": "false"})
	default:
		return nil
	}
}

func physicalModelFields() []fieldSpec {
	return []fieldSpec{{key: "name", label: "Physical model name", placeholder: "qwen-3.7-max"}, {key: "policy", label: "Source policy primitive", placeholder: "ordered-fallback"}, {key: "discoverable", label: "Expose in /v1/models", placeholder: "true | false"}}
}

func physicalModelEditFields() []fieldSpec { return physicalModelFields()[1:] }

func comboModelFields() []fieldSpec {
	return []fieldSpec{{key: "name", label: "Combo model name", placeholder: "junior"}, {key: "strategy", label: "Strategy primitive", placeholder: "ordered-fallback"}, {key: "discoverable", label: "Expose in /v1/models", placeholder: "true | false"}}
}

func comboModelEditFields() []fieldSpec { return comboModelFields()[1:] }

func newPhysicalModelForm(sources []routeReference) *formState {
	f := newResourceForm(int(sectionPhysical), nil, "")
	f.typedSources = append([]routeReference(nil), sources...)
	f.memberCursor = max(0, len(sources)-1)
	return f
}

func newComboModelForm(members []modelReference) *formState {
	f := newResourceForm(int(sectionComboModels), nil, "")
	f.typedMembers = append([]modelReference(nil), members...)
	f.memberCursor = max(0, len(members)-1)
	return f
}

func providerFields() []fieldSpec {
	return []fieldSpec{{key: "name", label: "Name", placeholder: "OpenRouter"}, {key: "prefix", label: "Prefix", placeholder: "openrouter"}, {key: "baseURL", label: "Base URL", placeholder: "https://api.example/v1"}, {key: "protocol", label: "Protocol", placeholder: "openai_chat | openai_responses | anthropic"}, {key: "definitionID", label: "Provider definition", placeholder: "openai-compatible-chat"}, {key: "modelsPath", label: "Models path", placeholder: "/models"}, {key: "authMode", label: "Auth mode", placeholder: "api_key"}}
}

func connectionFields() []fieldSpec {
	return []fieldSpec{{key: "providerNodeID", label: "Provider node ID", placeholder: "select a provider, then press c"}, {key: "name", label: "Name", placeholder: "personal key"}, {key: "credentialType", label: "Credential type", placeholder: "api_key"}, {key: "secret", label: "API key", placeholder: "secret is hidden", secret: true}, {key: "priority", label: "Priority", placeholder: "100"}}
}

func connectionEditFields() []fieldSpec {
	return []fieldSpec{{key: "name", label: "Name", placeholder: "personal key"}, {key: "credentialType", label: "Credential type", placeholder: "api_key"}, {key: "secret", label: "Replace API key (blank keeps current)", secret: true}, {key: "priority", label: "Priority", placeholder: "100"}}
}

func modelFields() []fieldSpec {
	return []fieldSpec{{key: "id", label: "Catalog ID (auto if blank)", placeholder: "generated on save"}, {key: "providerNodeID", label: "Provider node ID", placeholder: "choose provider first"}, {key: "kind", label: "Kind", placeholder: "custom"}, {key: "externalID", label: "Upstream model ID", placeholder: "model-name"}, {key: "displayName", label: "Display name", placeholder: "Model name"}}
}

func modelEditFields() []fieldSpec {
	return []fieldSpec{{key: "providerNodeID", label: "Provider node ID", placeholder: "provider"}, {key: "kind", label: "Kind", placeholder: "custom"}, {key: "externalID", label: "Upstream model ID", placeholder: "model-name"}, {key: "displayName", label: "Display name", placeholder: "Model name"}}
}

func logicalFields() []fieldSpec {
	return []fieldSpec{{key: "name", label: "Logical name", placeholder: "deepseek-v4-flash"}, {key: "targetRef", label: "Target reference", placeholder: "openrouter/model-id"}}
}

func comboFields() []fieldSpec {
	return []fieldSpec{{key: "name", label: "Canonical model name", placeholder: "qwen-3.7-max or junior"}, {key: "strategy", label: "Selection strategy", placeholder: "fallback | round_robin | round_robin_fallback | weighted"}, {key: "stickyLimit", label: "Sticky limit", placeholder: "1"}}
}

func comboEditFields() []fieldSpec {
	return []fieldSpec{{key: "strategy", label: "Strategy", placeholder: "fallback | round_robin | round_robin_fallback | weighted"}, {key: "stickyLimit", label: "Sticky limit", placeholder: "1"}}
}

func publishFields() []fieldSpec {
	return []fieldSpec{{key: "name", label: "Public model name", placeholder: "junior"}, {key: "targetRef", label: "Target reference", placeholder: "combo:junior"}, {key: "ownedBy", label: "Owned by", placeholder: "gobroom"}}
}

func publishEditFields() []fieldSpec {
	return []fieldSpec{{key: "targetRef", label: "Target reference", placeholder: "combo:junior"}, {key: "ownedBy", label: "Owned by", placeholder: "gobroom"}}
}

func (f *formState) Update(msg tea.Msg) (*formState, tea.Cmd, bool, map[string]any, error) {
	keyMsg, isKey := msg.(tea.KeyPressMsg)
	if isKey {
		key := keyMsg.String()
		if key == "esc" {
			if f.addingMember {
				f.addingMember = false
				f.memberInput.Blur()
				return f, nil, false, nil, nil
			}
			if f.memberMode {
				f.memberMode = false
				if len(f.inputs) > 0 {
					f.inputs[len(f.inputs)-1].Focus()
				}
				return f, nil, false, nil, nil
			}
			return nil, nil, false, nil, nil
		}
		if key == "ctrl+s" {
			params, err := f.Params()
			return f, nil, true, params, err
		}
		if f.hasMembers() {
			if f.addingMember {
				if key == "enter" {
					if err := f.addMember(strings.TrimSpace(f.memberInput.Value())); err != nil {
						return f, nil, false, nil, err
					}
					f.memberInput.Reset()
					f.memberInput.Blur()
					f.addingMember = false
					return f, nil, false, nil, nil
				}
				input, cmd := f.memberInput.Update(msg)
				f.memberInput = input
				return f, cmd, false, nil, nil
			}
			if f.memberMode {
				switch key {
				case "a":
					f.addingMember = true
					return f, f.memberInput.Focus(), false, nil, nil
				case "d", "x":
					f.removeMember()
					return f, nil, false, nil, nil
				case "K", "shift+k":
					f.moveMember(-1)
					return f, nil, false, nil, nil
				case "J", "shift+j":
					f.moveMember(1)
					return f, nil, false, nil, nil
				case "j", "down":
					f.memberCursor = min(f.memberCursor+1, max(0, f.memberCount()-1))
					return f, nil, false, nil, nil
				case "k", "up":
					f.memberCursor = max(f.memberCursor-1, 0)
					return f, nil, false, nil, nil
				case "esc", "left":
					f.memberMode = false
					f.inputs[max(0, len(f.inputs)-1)].Focus()
					return f, nil, false, nil, nil
				}
			}
			if key == "right" && f.active == len(f.fields)-1 {
				f.memberMode = true
				f.inputs[f.active].Blur()
				return f, nil, false, nil, nil
			}
		}
		if key == "tab" || key == "down" || key == "enter" {
			if f.active == len(f.inputs)-1 && f.hasMembers() {
				f.memberMode = true
				f.inputs[f.active].Blur()
				return f, nil, false, nil, nil
			}
			f.focus((f.active + 1) % len(f.inputs))
			return f, nil, false, nil, nil
		}
		if key == "shift+tab" || key == "up" {
			f.focus((f.active + len(f.inputs) - 1) % len(f.inputs))
			return f, nil, false, nil, nil
		}
	}
	input, cmd := f.inputs[f.active].Update(msg)
	f.inputs[f.active] = input
	return f, cmd, false, nil, nil
}

func (f *formState) focus(index int) {
	if len(f.inputs) == 0 {
		return
	}
	f.inputs[f.active].Blur()
	f.active = index
	f.inputs[f.active].Focus()
}

func (f *formState) moveMember(delta int) {
	to := f.memberCursor + delta
	if to < 0 || to >= f.memberCount() {
		return
	}
	switch f.method {
	case "physical_models.upsert":
		f.typedSources[f.memberCursor], f.typedSources[to] = f.typedSources[to], f.typedSources[f.memberCursor]
	case "combo_models.upsert":
		f.typedMembers[f.memberCursor], f.typedMembers[to] = f.typedMembers[to], f.typedMembers[f.memberCursor]
	default:
		f.comboMembers[f.memberCursor], f.comboMembers[to] = f.comboMembers[to], f.comboMembers[f.memberCursor]
	}
	f.memberCursor = to
}

func (f *formState) hasMembers() bool {
	return f.method == "combos.upsert" || f.method == "physical_models.upsert" || f.method == "combo_models.upsert"
}

func (f *formState) memberCount() int {
	switch f.method {
	case "physical_models.upsert":
		return len(f.typedSources)
	case "combo_models.upsert":
		return len(f.typedMembers)
	default:
		return len(f.comboMembers)
	}
}

func (f *formState) memberLabels() []string {
	switch f.method {
	case "physical_models.upsert":
		result := make([]string, 0, len(f.typedSources))
		for _, source := range f.typedSources {
			result = append(result, source.RouteID)
		}
		return result
	case "combo_models.upsert":
		result := make([]string, 0, len(f.typedMembers))
		for _, member := range f.typedMembers {
			result = append(result, member.Kind+":"+member.ID)
		}
		return result
	default:
		return append([]string(nil), f.comboMembers...)
	}
}

func (f *formState) addMember(raw string) error {
	if raw == "" {
		return nil
	}
	switch f.method {
	case "physical_models.upsert":
		f.typedSources = append(f.typedSources, routeReference{RouteID: raw})
	case "combo_models.upsert":
		var member modelReference
		if err := json.Unmarshal([]byte(raw), &member); err != nil {
			return fmt.Errorf("member must be JSON with kind and id: %w", err)
		}
		if (member.Kind != "physical" && member.Kind != "combo") || member.ID == "" {
			return fmt.Errorf("member kind must be physical or combo and id is required")
		}
		f.typedMembers = append(f.typedMembers, member)
	default:
		f.comboMembers = append(f.comboMembers, raw)
	}
	f.memberCursor = f.memberCount() - 1
	return nil
}

func (f *formState) removeMember() {
	if f.memberCount() == 0 {
		return
	}
	switch f.method {
	case "physical_models.upsert":
		f.typedSources = append(f.typedSources[:f.memberCursor], f.typedSources[f.memberCursor+1:]...)
	case "combo_models.upsert":
		f.typedMembers = append(f.typedMembers[:f.memberCursor], f.typedMembers[f.memberCursor+1:]...)
	default:
		f.comboMembers = append(f.comboMembers[:f.memberCursor], f.comboMembers[f.memberCursor+1:]...)
	}
	if f.memberCursor >= f.memberCount() {
		f.memberCursor = max(0, f.memberCount()-1)
	}
}

func (f *formState) Params() (map[string]any, error) {
	params := make(map[string]any, len(f.fields)+2)
	for index, field := range f.fields {
		value := strings.TrimSpace(f.inputs[index].Value())
		if field.key == "id" && f.method == "custom_models.upsert" && value == "" {
			id, err := newCatalogID()
			if err != nil {
				return nil, fmt.Errorf("generate catalog ID: %w", err)
			}
			value = id
		}
		if field.key == "id" && (f.method == "providers.update" || f.method == "connections.update") {
			params[field.key] = value
			continue
		}
		if field.key == "priority" || field.key == "stickyLimit" {
			if value == "" {
				if field.key == "priority" {
					continue
				}
				value = "1"
			}
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("%s must be an integer", field.label)
			}
			params[field.key] = number
			continue
		}
		if field.key == "discoverable" {
			exposed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("%s must be true or false", field.label)
			}
			params[field.key] = exposed
			continue
		}
		if field.key == "secret" && value == "" {
			continue
		}
		if value != "" {
			params[field.key] = value
		}
	}
	for key, value := range f.extra {
		params[key] = value
	}
	for _, required := range requiredFields(f.method) {
		if value, ok := params[required]; !ok || value == "" {
			return nil, fmt.Errorf("%s is required", required)
		}
	}
	if f.method == "custom_models.upsert" {
		if _, exists := params["providerNodeID"]; !exists {
			return nil, fmt.Errorf("choose a provider before adding a physical model")
		}
	}
	if f.method == "combos.upsert" {
		params["members"] = append([]string(nil), f.comboMembers...)
	}
	if f.method == "physical_models.upsert" {
		policy, _ := params["policy"].(string)
		delete(params, "policy")
		params["policy"] = strategySpec{ID: policy}
		params["sources"] = append([]routeReference(nil), f.typedSources...)
		if identity, ok := f.extra["identity"]; ok {
			params["identity"] = identity
		}
		if limits, ok := f.extra["limits"]; ok {
			params["limits"] = limits
		}
		if _, ok := params["enabled"]; !ok {
			params["enabled"] = true
		}
		if _, ok := params["profile"]; !ok {
			params["profile"] = map[string]capability{}
		}
	}
	if f.method == "combo_models.upsert" {
		strategy, _ := params["strategy"].(string)
		delete(params, "strategy")
		params["strategy"] = strategySpec{ID: strategy}
		params["members"] = append([]modelReference(nil), f.typedMembers...)
		if _, ok := params["enabled"]; !ok {
			params["enabled"] = true
		}
	}
	return params, nil
}

func requiredFields(method string) []string {
	switch method {
	case "providers.create":
		return []string{"name", "prefix", "baseURL", "protocol"}
	case "providers.update":
		return []string{"id"}
	case "connections.create":
		return []string{"providerNodeID", "name", "credentialType", "secret"}
	case "connections.update":
		return []string{"id"}
	case "custom_models.upsert":
		return []string{"id", "providerNodeID", "externalID", "displayName"}
	case "physical_models.upsert":
		return []string{"name", "policy"}
	case "combo_models.upsert":
		return []string{"name", "strategy"}
	default:
		return nil
	}
}

func newCatalogID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "model_" + hex.EncodeToString(value[:]), nil
}

func encodeMembersJSON(members []string) string {
	data, _ := json.Marshal(members)
	return string(data)
}
