package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
)

// setupFormBinding pins the complete authorization context and validator to
// the form that was constructed for one exact setup revision and action.
type setupFormBinding struct {
	SessionID string
	ActionID  string
	Revision  uint64
	SchemaRef extensions.Ref

	snapshot *extensions.Snapshot
	fields   []setupFieldBinding
	object   bool
}

type setupFieldBinding struct {
	name     string
	typeName string
	label    string
	hint     string
	required bool
	choices  []json.RawMessage
}

// newSetupActionForm creates a masked form for exactly one currently offered
// input action. Schema lookup and validation are entirely catalog-local.
func newSetupActionForm(view daemon.AuthorizationSetupView, action provider.AuthorizationAction, catalogJSON json.RawMessage) (*formState, error) {
	if view.Status != "awaiting_user" || view.SessionID == "" || view.Revision == 0 {
		return nil, fmt.Errorf("authorization setup is not awaiting user input")
	}
	if action.Kind != provider.AuthorizationInput || action.ID == "" || action.InputSchemaRef == nil {
		return nil, fmt.Errorf("authorization input action is incomplete")
	}
	offered := false
	for _, candidate := range view.Actions {
		if candidate.ID == action.ID && candidate.Kind == provider.AuthorizationInput && candidate.InputSchemaRef != nil && *candidate.InputSchemaRef == *action.InputSchemaRef {
			offered = true
			break
		}
	}
	if !offered {
		return nil, fmt.Errorf("authorization input action is not offered")
	}
	ref := *action.InputSchemaRef
	if err := ref.Validate(); err != nil || ref.Kind != extensions.SchemaKind {
		return nil, fmt.Errorf("authorization input schema reference is invalid")
	}
	var catalogView extensions.CatalogView
	if err := json.Unmarshal(catalogJSON, &catalogView); err != nil {
		return nil, fmt.Errorf("authorization input schema catalog is invalid")
	}
	catalog := extensions.NewCatalog()
	var schemaDocument json.RawMessage
	found := false
	for _, schema := range catalogView.Schemas {
		if err := catalog.RegisterSchema(schema.Ref, schema.Document); err != nil {
			return nil, fmt.Errorf("authorization input schema catalog is invalid")
		}
		if schema.Ref == ref {
			schemaDocument = append(json.RawMessage(nil), schema.Document...)
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("authorization input schema is unavailable")
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		return nil, fmt.Errorf("authorization input schema catalog is invalid")
	}
	binding := &setupFormBinding{SessionID: view.SessionID, ActionID: action.ID, Revision: view.Revision, SchemaRef: ref, snapshot: snapshot}
	fields, useProperties := simpleSetupProperties(schemaDocument)
	if !useProperties {
		binding.fields = []setupFieldBinding{{name: "value", typeName: "object"}}
		form := buildForm(action.Label, "auth.setup.advance", []fieldSpec{{key: "setup:object", label: "Input object (JSON)", placeholder: "{}", secret: true}}, nil)
		form.inputs[0].CharLimit = provider.MaxAuthorizationStateBytes
		binding.object = true
		form.setup = binding
		return form, nil
	}

	binding.fields = fields
	specs := make([]fieldSpec, 0, len(fields))
	for _, field := range fields {
		specs = append(specs, fieldSpec{key: "setup:" + field.name, label: field.label, placeholder: field.hint, secret: true})
	}
	if len(specs) == 0 {
		// An empty properties map still needs a way to submit the full object.
		binding.fields = []setupFieldBinding{{name: "value", typeName: "object"}}
		binding.object = true
		specs = []fieldSpec{{key: "setup:object", label: "Input object (JSON)", placeholder: "{}", secret: true}}
	}
	form := buildForm(action.Label, "auth.setup.advance", specs, nil)
	for i := range form.inputs {
		form.inputs[i].CharLimit = provider.MaxAuthorizationStateBytes
	}
	form.setup = binding
	return form, nil
}

// The UI can represent a closed set of named properties without interpreting
// constraints such as oneOf or dependentRequired. The frozen validator remains
// the authority; open/ref-only shapes use a lossless JSON-object editor.
func simpleSetupProperties(document json.RawMessage) ([]setupFieldBinding, bool) {
	var root struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
		PatternProperties    map[string]json.RawMessage `json:"patternProperties"`
	}
	if json.Unmarshal(document, &root) != nil || root.Type != "object" {
		return nil, false
	}
	if len(root.Properties) > 32 {
		return nil, false
	}
	var additional bool
	if json.Unmarshal(root.AdditionalProperties, &additional) != nil || additional || len(root.PatternProperties) > 0 {
		return nil, false
	}
	required := make(map[string]bool, len(root.Required))
	for _, name := range root.Required {
		required[name] = true
	}
	fields := make([]setupFieldBinding, 0, len(root.Properties))
	for name, propertyRaw := range root.Properties {
		var property struct {
			Type        string            `json:"type"`
			Title       string            `json:"title"`
			Description string            `json:"description"`
			Enum        []json.RawMessage `json:"enum"`
		}
		if json.Unmarshal(propertyRaw, &property) != nil {
			return nil, false
		}
		if property.Type == "" && len(property.Enum) > 0 {
			property.Type = setupChoiceType(property.Enum[0])
			for _, choice := range property.Enum[1:] {
				if setupChoiceType(choice) != property.Type {
					return nil, false
				}
			}
		}
		if property.Type != "string" && property.Type != "integer" && property.Type != "number" && property.Type != "boolean" && property.Type != "object" && property.Type != "array" {
			return nil, false
		}
		label := property.Title
		if label == "" {
			label = name
		}
		hint := property.Description
		if hint == "" {
			hint = property.Type
		}
		if len(property.Enum) > 0 {
			hint += " · ctrl+n/p cycles declared options"
		}
		fields = append(fields, setupFieldBinding{name: name, typeName: property.Type, label: label, hint: hint, required: required[name], choices: property.Enum})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	return fields, true
}

func setupChoiceType(raw json.RawMessage) string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return ""
	}
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	}
	return ""
}

func (f *formState) setupActionParams() (map[string]any, error) {
	binding := f.setup
	if binding == nil || binding.snapshot == nil || f.method != "auth.setup.advance" {
		return nil, fmt.Errorf("authorization input form is unavailable")
	}
	value, err := binding.payload(f)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 || len(value) > provider.MaxAuthorizationStateBytes {
		return nil, fmt.Errorf("authorization input must be within the allowed size")
	}
	if err := binding.snapshot.ValidateSchema(binding.SchemaRef, value); err != nil {
		return nil, fmt.Errorf("authorization input does not match the required schema")
	}
	return map[string]any{
		"sessionID": binding.SessionID,
		"actionID":  binding.ActionID,
		"revision":  binding.Revision,
		"value":     json.RawMessage(value),
	}, nil
}

func (b *setupFormBinding) payload(form *formState) (json.RawMessage, error) {
	if b.object {
		return decodeSetupObject(form.inputs[0].Value())
	}
	value := make(map[string]any, len(b.fields))
	for i, field := range b.fields {
		text := form.inputs[i].Value()
		if text == "" && !field.required {
			continue
		}
		switch field.typeName {
		case "string":
			value[field.name] = text
		case "integer", "number", "boolean", "object", "array":
			decoder := json.NewDecoder(strings.NewReader(text))
			decoder.UseNumber()
			var parsed any
			if err := decoder.Decode(&parsed); err != nil {
				return nil, fmt.Errorf("authorization input has an invalid typed field")
			}
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				return nil, fmt.Errorf("authorization input has an invalid typed field")
			}
			value[field.name] = parsed
		default:
			return nil, fmt.Errorf("authorization input form is invalid")
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("authorization input could not be encoded")
	}
	return encoded, nil
}

// Only registered, public enum choices can be displayed as feedback. Arbitrary
// text from Value() is never returned to the app's status/inspector.
func (f *formState) cycleSetupOption(delta int) string {
	if f.setup == nil || f.setup.object || f.active < 0 || f.active >= len(f.setup.fields) {
		return ""
	}
	field := f.setup.fields[f.active]
	if len(field.choices) == 0 {
		return ""
	}
	current := -1
	texts := make([]string, len(field.choices))
	for i, raw := range field.choices {
		if field.typeName == "string" {
			if json.Unmarshal(raw, &texts[i]) != nil {
				return ""
			}
		} else {
			texts[i] = string(raw)
		}
		if f.inputs[f.active].Value() == texts[i] {
			current = i
		}
	}
	next := 0
	if current < 0 {
		if delta < 0 {
			next = len(texts) - 1
		}
	} else {
		next = (current + delta + len(texts)) % len(texts)
	}
	f.inputs[f.active].SetValue(texts[next])
	return texts[next]
}

func decodeSetupObject(text string) (json.RawMessage, error) {
	if len(text) == 0 || len(text) > provider.MaxAuthorizationStateBytes {
		return nil, fmt.Errorf("authorization input must be within the allowed size")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("authorization input must be one JSON object")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("authorization input must be one JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("authorization input must be one JSON object")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("authorization input must be one JSON object")
	}
	return encoded, nil
}

// wipeSetupInput clears retained secret text when the owner closes or replaces
// the form. Lifecycle callers intentionally remain in app.go.
func (f *formState) wipeSetupInput() {
	if f == nil {
		return
	}
	for i := range f.inputs {
		f.inputs[i].SetValue("")
		f.inputs[i].Blur()
	}
	f.active = 0
	f.setup = nil
}
