package kernel

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/fm39hz/gobroom/internal/extensions"
)

const StrategyExtensionKind = "strategy"

func StrategyRef(id string, contractVersion uint64) extensions.Ref {
	return extensions.Ref{Kind: StrategyExtensionKind, ID: id, ContractVersion: contractVersion}
}

type StrategyOptionType string

const StrategyOptionInteger StrategyOptionType = "integer"

type StrategyOptionDefinition struct {
	Key         string             `json:"key"`
	Label       string             `json:"label"`
	Description string             `json:"description,omitempty"`
	Type        StrategyOptionType `json:"type"`
	Default     any                `json:"default,omitempty"`
	Minimum     int                `json:"minimum,omitempty"`
	Maximum     int                `json:"maximum,omitempty"`
}

// StrategyDefinition is the single built-in strategy contract used by the
// scheduler, configuration validation, IPC catalog and user interfaces.
// Runtime is intentionally omitted from JSON; clients select by stable ID.
type StrategyDefinition struct {
	Ref         extensions.Ref             `json:"ref"`
	ID          string                     `json:"id"`
	Label       string                     `json:"label"`
	Description string                     `json:"description"`
	Options     []StrategyOptionDefinition `json:"options,omitempty"`
	Runtime     Strategy                   `json:"-"`
	Primitive   StrategyPrimitive          `json:"-"`
}

var builtinStrategyDefinitions = []StrategyDefinition{
	{
		Ref: extensions.Ref{Kind: StrategyExtensionKind, ID: "ordered-fallback", ContractVersion: 1},
		ID:  "ordered-fallback", Label: "Ordered fallback",
		Description: "Try members in their configured order; continue only for retryable failures.",
		Runtime:     StrategyFallback, Primitive: OrderedFallback{},
	},
	{
		Ref: extensions.Ref{Kind: StrategyExtensionKind, ID: "rotating-fallback", ContractVersion: 1},
		ID:  "rotating-fallback", Label: "Rotating fallback",
		Description: "Rotate the first member on each request, then try the remaining members in order.",
		Runtime:     StrategyRotatingFallback, Primitive: RotatingFallback{},
	},
	{
		Ref: extensions.Ref{Kind: StrategyExtensionKind, ID: "round-robin", ContractVersion: 1},
		ID:  "round-robin", Label: "Round robin",
		Description: "Choose one member per request; on failure, control returns to the parent Combo policy rather than trying sibling members.",
		Runtime:     StrategyRoundRobin, Primitive: RoundRobin{},
	},
	{
		Ref: extensions.Ref{Kind: StrategyExtensionKind, ID: "round-robin-fallback", ContractVersion: 1},
		ID:  "round-robin-fallback", Label: "Sticky round-robin fallback",
		Description: "Keep the current starting member for a bounded number of requests, then rotate and fall back in order.",
		Runtime:     StrategyRoundRobinFallback, Primitive: StickyRoundRobinFallback{},
		Options: []StrategyOptionDefinition{{
			Key: "stickyLimit", Label: "Requests per starting member",
			Description: "How many requests use one starting member before rotating.",
			Type:        StrategyOptionInteger, Default: 1, Minimum: 1, Maximum: 1000,
		}},
	},
	{
		Ref: extensions.Ref{Kind: StrategyExtensionKind, ID: "weighted-fallback", ContractVersion: 1},
		ID:  "weighted-fallback", Label: "Weighted fallback",
		Description: "Choose a weighted starting member, then try remaining members as fallbacks; weights are edited on member edges.",
		Runtime:     StrategyWeighted, Primitive: WeightedFallback{},
	},
}

// RegisterStrategyExtension adds a strategy implementation to the shared
// extension catalog. Its option schema is compiled and versioned with the
// descriptor; the factory returns the immutable strategy module, while each
// model binding carries validated options separately.
func RegisterStrategyExtension(catalog *extensions.Catalog, definition StrategyDefinition) error {
	if catalog == nil {
		return fmt.Errorf("strategy extension catalog is nil")
	}
	if err := definition.Ref.Validate(); err != nil || definition.Ref.Kind != StrategyExtensionKind || definition.ID != definition.Ref.ID {
		return fmt.Errorf("strategy requires an exact %q ref whose ID matches the strategy ID", StrategyExtensionKind)
	}
	if definition.Runtime == "" || definition.Primitive == nil || definition.Label == "" || definition.Description == "" {
		return fmt.Errorf("strategy %q requires label, description and runtime implementation", definition.Ref.Key())
	}
	if err := ValidateStrategyOptions(definition.ID, definition.Options); err != nil {
		return err
	}
	schemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.strategy." + definition.ID + ".options", ContractVersion: definition.Ref.ContractVersion}
	shape, err := strategyOptionsSchema(definition.Options)
	if err != nil {
		return fmt.Errorf("strategy %q options schema: %w", definition.Ref.Key(), err)
	}
	document, err := extensions.BindSchemaDocument(schemaRef, shape)
	if err != nil {
		return err
	}
	if err := catalog.RegisterSchema(schemaRef, document); err != nil {
		return err
	}
	definition.Options = cloneStrategyOptions(definition.Options)
	return catalog.Register(extensions.Descriptor{
		Ref: definition.Ref, ImplementationVersion: fmt.Sprintf("contract-%d", definition.Ref.ContractVersion),
		DisplayName: definition.Label, Description: definition.Description, OptionsSchemaRef: &schemaRef,
		LifecycleCapabilities: []string{"model.policy.strategy"},
	}, func(json.RawMessage) (any, error) { return definition, nil })
}

func RegisterBuiltinStrategyExtensions(catalog *extensions.Catalog) error {
	for _, definition := range builtinStrategyDefinitions {
		if err := RegisterStrategyExtension(catalog, definition); err != nil {
			return err
		}
	}
	return nil
}

func StrategyDefinitionsFromCatalog(catalog *extensions.Snapshot) ([]StrategyDefinition, error) {
	if catalog == nil {
		return nil, fmt.Errorf("strategy extension snapshot is nil")
	}
	definitions := make([]StrategyDefinition, 0)
	runtimeRefs := map[Strategy]extensions.Ref{}
	for _, descriptor := range catalog.Descriptors() {
		if descriptor.Ref.Kind != StrategyExtensionKind {
			continue
		}
		_, implementation, err := catalog.Implementation(descriptor.Ref)
		if err != nil {
			return nil, err
		}
		definition, ok := implementation.(StrategyDefinition)
		if !ok || definition.Ref != descriptor.Ref || definition.ID != descriptor.Ref.ID || definition.Runtime == "" || definition.Primitive == nil {
			return nil, fmt.Errorf("strategy %q factory returned invalid implementation %T", descriptor.Ref.Key(), implementation)
		}
		if err := ValidateStrategyOptions(definition.ID, definition.Options); err != nil {
			return nil, fmt.Errorf("strategy %q: %w", descriptor.Ref.Key(), err)
		}
		if previous, exists := runtimeRefs[definition.Runtime]; exists && previous != definition.Ref {
			return nil, fmt.Errorf("strategy refs %q and %q share runtime key %q", previous.Key(), definition.Ref.Key(), definition.Runtime)
		}
		runtimeRefs[definition.Runtime] = definition.Ref
		definitions = append(definitions, cloneStrategyDefinition(definition))
	}
	return definitions, nil
}

// StrategyCatalog is the immutable, exact-ref view used by configuration
// validation and snapshot compilation. It never resolves by strategy ID alone.
type StrategyCatalog struct {
	snapshot    *extensions.Snapshot
	definitions map[extensions.Ref]StrategyDefinition
}

func NewStrategyCatalog(snapshot *extensions.Snapshot) (*StrategyCatalog, error) {
	definitions, err := StrategyDefinitionsFromCatalog(snapshot)
	if err != nil {
		return nil, err
	}
	catalog := &StrategyCatalog{snapshot: snapshot, definitions: make(map[extensions.Ref]StrategyDefinition, len(definitions))}
	for _, definition := range definitions {
		catalog.definitions[definition.Ref] = definition
	}
	return catalog, nil
}

func NewBuiltinStrategyCatalog() (*StrategyCatalog, error) {
	catalog := extensions.NewCatalog()
	if err := RegisterBuiltinStrategyExtensions(catalog); err != nil {
		return nil, err
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		return nil, err
	}
	return NewStrategyCatalog(snapshot)
}

func (c *StrategyCatalog) Definitions() []StrategyDefinition {
	if c == nil {
		return nil
	}
	definitions := make([]StrategyDefinition, 0, len(c.definitions))
	for _, definition := range c.definitions {
		definitions = append(definitions, cloneStrategyDefinition(definition))
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Ref.Key() < definitions[j].Ref.Key() })
	return definitions
}

// Extensions exposes the frozen shared catalog used to resolve this strategy
// registry. Portable configuration locks use it to pin all compiled contracts.
func (c *StrategyCatalog) Extensions() *extensions.Snapshot {
	if c == nil {
		return nil
	}
	return c.snapshot
}

func (c *StrategyCatalog) Definition(ref extensions.Ref) (StrategyDefinition, bool) {
	if c == nil {
		return StrategyDefinition{}, false
	}
	definition, ok := c.definitions[ref]
	return cloneStrategyDefinition(definition), ok
}

func (c *StrategyCatalog) Validate(ref extensions.Ref, config map[string]any) error {
	if c == nil || c.snapshot == nil {
		return fmt.Errorf("strategy catalog is unavailable")
	}
	if _, ok := c.definitions[ref]; !ok {
		return fmt.Errorf("unknown exact strategy ref %q", ref.Key())
	}
	options, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode options for strategy %q: %w", ref.Key(), err)
	}
	if config == nil {
		options = json.RawMessage(`{}`)
	}
	return c.snapshot.ValidateOptions(ref, options)
}

func (c *StrategyCatalog) DefaultOptions(ref extensions.Ref) (map[string]any, error) {
	definition, ok := c.Definition(ref)
	if !ok {
		return nil, fmt.Errorf("unknown exact strategy ref %q", ref.Key())
	}
	options := make(map[string]any, len(definition.Options))
	for _, option := range definition.Options {
		if option.Default != nil {
			options[option.Key] = option.Default
		}
	}
	return options, nil
}

func (c *StrategyCatalog) Resolve(ref extensions.Ref, config map[string]any) (StrategyDefinition, int, error) {
	definition, ok := c.Definition(ref)
	if !ok {
		return StrategyDefinition{}, 0, fmt.Errorf("unknown exact strategy ref %q", ref.Key())
	}
	if err := c.Validate(ref, config); err != nil {
		return StrategyDefinition{}, 0, err
	}
	stickyLimit := 1
	for _, option := range definition.Options {
		if option.Key != "stickyLimit" {
			continue
		}
		value := anyToIntOption(config[option.Key])
		if config[option.Key] == nil {
			value = anyToIntOption(option.Default)
		}
		if value != nil {
			stickyLimit = *value
		}
	}
	return definition, stickyLimit, nil
}

func anyToIntOption(value any) *int {
	if number, ok := integerOptionValue(value); ok {
		return &number
	}
	return nil
}

func cloneStrategyOptions(options []StrategyOptionDefinition) []StrategyOptionDefinition {
	return append([]StrategyOptionDefinition(nil), options...)
}

func cloneStrategyDefinition(definition StrategyDefinition) StrategyDefinition {
	definition.Options = cloneStrategyOptions(definition.Options)
	return definition
}

func strategyOptionsSchema(options []StrategyOptionDefinition) (json.RawMessage, error) {
	properties := make(map[string]any, len(options))
	for _, option := range options {
		field := map[string]any{"type": "integer", "minimum": option.Minimum}
		if option.Maximum > 0 {
			field["maximum"] = option.Maximum
		}
		if option.Default != nil {
			field["default"] = option.Default
		}
		properties[option.Key] = field
	}
	return json.Marshal(map[string]any{"type": "object", "properties": properties, "additionalProperties": false})
}

func ValidateStrategyOptions(id string, options []StrategyOptionDefinition) error {
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		if option.Key == "" || option.Label == "" || option.Type != StrategyOptionInteger || seen[option.Key] {
			return fmt.Errorf("strategy %q has an invalid or duplicate option definition", id)
		}
		if option.Maximum > 0 && option.Minimum > option.Maximum {
			return fmt.Errorf("strategy %q option %q has an invalid range", id, option.Key)
		}
		seen[option.Key] = true
	}
	return nil
}

func StrategyDefinitions() []StrategyDefinition {
	result := make([]StrategyDefinition, 0, len(builtinStrategyDefinitions))
	for _, definition := range builtinStrategyDefinitions {
		result = append(result, cloneStrategyDefinition(definition))
	}
	return result
}

func integerOptionValue(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int8:
		return int(number), true
	case int16:
		return int(number), true
	case int32:
		return int(number), true
	case int64:
		return int(number), int64(int(number)) == number
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number > math.MaxInt || number < math.MinInt {
			return 0, false
		}
		return int(number), true
	default:
		return 0, false
	}
}
