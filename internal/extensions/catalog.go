// Package extensions owns versioned contracts shared by provider, operation,
// feature, policy, auth and transform modules. It contains no dispatch logic.
package extensions

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	SchemaKind = "schema"
	schemaHost = "schemas.gobroom.invalid"
	draft2020  = "https://json-schema.org/draft/2020-12/schema"
)

// Ref selects one exact contract. Contract versions are positive integers;
// resolving a missing version never falls back to another version.
type Ref struct {
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	ContractVersion uint64 `json:"contractVersion"`
}

func (r Ref) Validate() error {
	if strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.ID) == "" || r.ContractVersion == 0 {
		return fmt.Errorf("extension reference requires kind, ID and positive contractVersion")
	}
	if strings.ContainsAny(r.Kind+r.ID, "\x00\r\n") {
		return fmt.Errorf("extension reference contains a control character")
	}
	return nil
}

func (r Ref) Key() string { return r.Kind + ":" + r.ID + "@" + fmt.Sprint(r.ContractVersion) }

func (r Ref) URI() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	if r.Kind != SchemaKind {
		return "", fmt.Errorf("URI is only defined for schema references")
	}
	return schemaURI(r), nil
}

type ResourceBounds struct {
	MaxInputBytes    int64 `json:"maxInputBytes,omitempty"`
	MaxOutputBytes   int64 `json:"maxOutputBytes,omitempty"`
	MaxBufferedBytes int64 `json:"maxBufferedBytes,omitempty"`
	DeadlineMillis   int64 `json:"deadlineMillis,omitempty"`
}

// ArtifactInput describes one named artifact port on a versioned operation.
type ArtifactInput struct {
	Role     string `json:"role"`
	TypeRef  Ref    `json:"typeRef"`
	MinCount int    `json:"minCount"`
	MaxCount int    `json:"maxCount"`
}

// ArtifactOutput declares a named, bounded artifact port emitted by an
// operation response. Response bodies remain leases; only their typed refs
// cross canonical events and client renderers.
type ArtifactOutput struct {
	Role     string `json:"role"`
	TypeRef  Ref    `json:"typeRef"`
	MinCount int    `json:"minCount"`
	MaxCount int    `json:"maxCount"`
}

type Descriptor struct {
	Ref                   Ref              `json:"ref"`
	ImplementationVersion string           `json:"implementationVersion"`
	DisplayName           string           `json:"displayName"`
	Description           string           `json:"description"`
	OptionsSchemaRef      *Ref             `json:"optionsSchemaRef,omitempty"`
	InputSchemaRef        *Ref             `json:"inputSchemaRef,omitempty"`
	ResultSchemaRef       *Ref             `json:"resultSchemaRef,omitempty"`
	EventSchemaRefs       []Ref            `json:"eventSchemaRefs,omitempty"`
	ReplaySafety          string           `json:"replaySafety,omitempty"`
	Dependencies          []Ref            `json:"dependencies,omitempty"`
	SemanticContracts     []Ref            `json:"semanticContracts,omitempty"`
	ArtifactInputs        []ArtifactInput  `json:"artifactInputs,omitempty"`
	ArtifactOutputs       []ArtifactOutput `json:"artifactOutputs,omitempty"`
	LifecycleCapabilities []string         `json:"lifecycleCapabilities,omitempty"`
	FailureModes          []string         `json:"failureModes,omitempty"`
	ResourceBounds        ResourceBounds   `json:"resourceBounds"`
	ArtifactPolicy        *ArtifactPolicy  `json:"artifactPolicy,omitempty"`
	SetupView             json.RawMessage  `json:"setupView,omitempty"`
}

type Schema struct {
	Ref      Ref             `json:"ref"`
	Digest   string          `json:"digest"`
	Document json.RawMessage `json:"document"`
}

type CatalogView struct {
	Fingerprint string       `json:"fingerprint"`
	Descriptors []Descriptor `json:"descriptors"`
	Schemas     []Schema     `json:"schemas"`
}

// DependencyLock pins the exact compiled extension closure used by a portable
// configuration. It records runtime roots, transitive descriptors and schema
// digests, but never implementation binaries or secret material.
type DependencyLock struct {
	Version    int            `json:"version"`
	Roots      []Ref          `json:"roots"`
	Extensions []ExtensionPin `json:"extensions"`
	Schemas    []SchemaPin    `json:"schemas"`
}

type ExtensionPin struct {
	Ref                   Ref    `json:"ref"`
	ImplementationVersion string `json:"implementationVersion"`
	DescriptorSHA256      string `json:"descriptorSha256"`
}

type SchemaPin struct {
	Ref    Ref    `json:"ref"`
	SHA256 string `json:"sha256"`
}

type Factory func(json.RawMessage) (any, error)

type registration struct {
	descriptor Descriptor
	factory    Factory
}

type Catalog struct {
	mu            sync.Mutex
	schemas       map[Ref]Schema
	registrations map[Ref]registration
	frozen        bool
	snapshot      *Snapshot
}

func NewCatalog() *Catalog {
	return &Catalog{schemas: map[Ref]Schema{}, registrations: map[Ref]registration{}}
}

// BindSchemaDocument attaches the registry's immutable resource URI and the
// supported JSON Schema dialect to a typed JSON object schema body.
func BindSchemaDocument(ref Ref, shape json.RawMessage) (json.RawMessage, error) {
	uri, err := ref.URI()
	if err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(shape, &document); err != nil {
		return nil, fmt.Errorf("decode schema body: %w", err)
	}
	if document == nil {
		return nil, fmt.Errorf("schema body must be a JSON object")
	}
	for _, reserved := range []string{"$schema", "$id"} {
		if _, exists := document[reserved]; exists {
			return nil, fmt.Errorf("schema body cannot override %s", reserved)
		}
	}
	id, _ := json.Marshal(uri)
	draft, _ := json.Marshal(draft2020)
	document["$id"] = id
	document["$schema"] = draft
	return json.Marshal(document)
}

// RegisterSchema stores a local, versioned JSON Schema resource. Only the
// pinned 2020-12 dialect and registry-local $ref resources are accepted.
func (c *Catalog) RegisterSchema(ref Ref, document json.RawMessage) error {
	if c == nil {
		return fmt.Errorf("extension catalog is nil")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Kind != SchemaKind {
		return fmt.Errorf("schema resource %q must use kind %q", ref.Key(), SchemaKind)
	}
	canonical, parsed, err := canonicalJSON(document)
	if err != nil {
		return fmt.Errorf("schema %q: %w", ref.Key(), err)
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return fmt.Errorf("schema %q must be a JSON object", ref.Key())
	}
	if object["$schema"] != draft2020 {
		return fmt.Errorf("schema %q must declare JSON Schema Draft 2020-12", ref.Key())
	}
	if object["$id"] != schemaURI(ref) {
		return fmt.Errorf("schema %q must declare registry URI %q in $id", ref.Key(), schemaURI(ref))
	}
	digestBytes := sha256.Sum256(canonical)
	resource := Schema{Ref: ref, Digest: hex.EncodeToString(digestBytes[:]), Document: canonical}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return fmt.Errorf("extension catalog is frozen")
	}
	if existing, exists := c.schemas[ref]; exists {
		if existing.Digest == resource.Digest {
			return nil
		}
		return fmt.Errorf("schema %q is already registered with digest %s", ref.Key(), existing.Digest)
	}
	c.schemas[ref] = resource
	return nil
}

func (c *Catalog) Register(descriptor Descriptor, factory Factory) error {
	if factory == nil {
		return fmt.Errorf("extension factory is required")
	}
	prepared, err := prepareDescriptor(descriptor)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("extension catalog is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return fmt.Errorf("extension catalog is frozen")
	}
	if _, exists := c.registrations[prepared.Ref]; exists {
		return fmt.Errorf("extension %q is already registered", prepared.Ref.Key())
	}
	c.registrations[prepared.Ref] = registration{descriptor: prepared, factory: factory}
	return nil
}

func (c *Catalog) RegisterDescriptor(descriptor Descriptor) error {
	prepared, err := prepareDescriptor(descriptor)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("extension catalog is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return fmt.Errorf("extension catalog is frozen")
	}
	if _, exists := c.registrations[prepared.Ref]; exists {
		return fmt.Errorf("extension %q is already registered", prepared.Ref.Key())
	}
	c.registrations[prepared.Ref] = registration{descriptor: prepared}
	return nil
}

func prepareDescriptor(descriptor Descriptor) (Descriptor, error) {
	if err := descriptor.Ref.Validate(); err != nil {
		return Descriptor{}, err
	}
	if descriptor.Ref.Kind == SchemaKind {
		return Descriptor{}, fmt.Errorf("schema descriptors must use RegisterSchema")
	}
	if strings.TrimSpace(descriptor.ImplementationVersion) == "" || strings.TrimSpace(descriptor.DisplayName) == "" || strings.TrimSpace(descriptor.Description) == "" {
		return Descriptor{}, fmt.Errorf("extension %q requires implementation version and display metadata", descriptor.Ref.Key())
	}
	if err := validateBounds(descriptor); err != nil {
		return Descriptor{}, fmt.Errorf("extension %q: %w", descriptor.Ref.Key(), err)
	}
	if len(descriptor.ArtifactInputs) > 0 && descriptor.Ref.Kind != "operation" {
		return Descriptor{}, fmt.Errorf("only operation extensions may declare artifact input ports")
	}
	if len(descriptor.ArtifactOutputs) > 0 && descriptor.Ref.Kind != "operation" {
		return Descriptor{}, fmt.Errorf("only operation extensions may declare artifact output ports")
	}
	if len(descriptor.FailureModes) > 0 && descriptor.Ref.Kind != "request_transform" && descriptor.Ref.Kind != "response_transform" {
		return Descriptor{}, fmt.Errorf("only request and response transforms may declare failure modes")
	}
	seenFailureModes := map[string]bool{}
	for _, mode := range descriptor.FailureModes {
		if mode != "safe_fail_open" || seenFailureModes[mode] {
			return Descriptor{}, fmt.Errorf("extension %q has an unsupported or duplicate failure mode %q", descriptor.Ref.Key(), mode)
		}
		seenFailureModes[mode] = true
	}
	seenInputRoles := map[string]bool{}
	for _, input := range descriptor.ArtifactInputs {
		if strings.TrimSpace(input.Role) == "" || seenInputRoles[input.Role] || input.MinCount < 0 || input.MaxCount <= 0 || input.MinCount > input.MaxCount {
			return Descriptor{}, fmt.Errorf("extension %q has an invalid or duplicate artifact input role", descriptor.Ref.Key())
		}
		if err := input.TypeRef.Validate(); err != nil || input.TypeRef.Kind != ArtifactKind {
			return Descriptor{}, fmt.Errorf("extension %q artifact input %q requires an exact artifact type", descriptor.Ref.Key(), input.Role)
		}
		seenInputRoles[input.Role] = true
	}
	seenOutputRoles := map[string]bool{}
	for _, output := range descriptor.ArtifactOutputs {
		if strings.TrimSpace(output.Role) == "" || seenOutputRoles[output.Role] || output.MinCount < 0 || output.MaxCount <= 0 || output.MinCount > output.MaxCount {
			return Descriptor{}, fmt.Errorf("extension %q has an invalid or duplicate artifact output role", descriptor.Ref.Key())
		}
		if err := output.TypeRef.Validate(); err != nil || output.TypeRef.Kind != ArtifactKind {
			return Descriptor{}, fmt.Errorf("extension %q artifact output %q requires an exact artifact type", descriptor.Ref.Key(), output.Role)
		}
		seenOutputRoles[output.Role] = true
	}
	if descriptor.Ref.Kind == ArtifactKind {
		if descriptor.ArtifactPolicy == nil || descriptor.ResourceBounds.MaxInputBytes <= 0 {
			return Descriptor{}, fmt.Errorf("artifact extension %q requires an artifact policy and positive maxInputBytes", descriptor.Ref.Key())
		}
		if err := validateArtifactPolicy(*descriptor.ArtifactPolicy); err != nil {
			return Descriptor{}, fmt.Errorf("artifact extension %q: %w", descriptor.Ref.Key(), err)
		}
	} else if descriptor.ArtifactPolicy != nil {
		return Descriptor{}, fmt.Errorf("non-artifact extension %q cannot declare artifact policy", descriptor.Ref.Key())
	}
	if len(descriptor.SetupView) > 0 {
		canonical, _, err := canonicalJSON(descriptor.SetupView)
		if err != nil {
			return Descriptor{}, fmt.Errorf("extension %q setup view: %w", descriptor.Ref.Key(), err)
		}
		descriptor.SetupView = canonical
	}
	return cloneDescriptor(descriptor), nil
}

func (c *Catalog) RegisterFactory(ref Ref, factory Factory) error {
	return c.putFactory(ref, factory, false)
}

// ReplaceFactory swaps an implementation during daemon composition, before
// the catalog is frozen. It is intended for an explicit implementation
// override such as installing the daemon's durable session store.
func (c *Catalog) ReplaceFactory(ref Ref, factory Factory) error {
	return c.putFactory(ref, factory, true)
}

func (c *Catalog) putFactory(ref Ref, factory Factory, replace bool) error {
	if c == nil || factory == nil {
		return fmt.Errorf("extension catalog and factory are required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return fmt.Errorf("extension catalog is frozen")
	}
	item, ok := c.registrations[ref]
	if !ok {
		return fmt.Errorf("extension descriptor %q is not registered", ref.Key())
	}
	if item.factory != nil && !replace {
		return fmt.Errorf("extension factory %q is already registered", ref.Key())
	}
	item.factory = factory
	c.registrations[ref] = item
	return nil
}

// Freeze resolves exact references/dependencies, checks for cycles and
// compiles schemas before configuration can be published or requests served.
func (c *Catalog) Freeze() (*Snapshot, error) {
	if c == nil {
		return nil, fmt.Errorf("extension catalog is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		return c.snapshot, nil
	}
	if err := c.validateGraph(); err != nil {
		return nil, err
	}
	for ref, item := range c.registrations {
		if item.factory == nil {
			return nil, fmt.Errorf("extension %q has no registered implementation factory", ref.Key())
		}
	}
	compiled, err := c.compileSchemas()
	if err != nil {
		return nil, err
	}
	entries := make(map[Ref]registration, len(c.registrations))
	for ref, item := range c.registrations {
		entries[ref] = registration{descriptor: cloneDescriptor(item.descriptor), factory: item.factory}
	}
	schemas := make(map[Ref]Schema, len(c.schemas))
	for ref, schema := range c.schemas {
		schema.Document = append(json.RawMessage(nil), schema.Document...)
		schemas[ref] = schema
	}
	c.frozen = true
	c.snapshot = &Snapshot{registrations: entries, schemas: schemas, compiled: compiled, fingerprint: c.fingerprint()}
	return c.snapshot, nil
}

type Snapshot struct {
	registrations map[Ref]registration
	schemas       map[Ref]Schema
	compiled      map[Ref]*jsonschema.Schema
	fingerprint   string
}

func (s *Snapshot) View() CatalogView {
	if s == nil {
		return CatalogView{}
	}
	view := CatalogView{Fingerprint: s.fingerprint, Descriptors: s.Descriptors()}
	refs := make([]Ref, 0, len(s.schemas))
	for ref := range s.schemas {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key() < refs[j].Key() })
	view.Schemas = make([]Schema, 0, len(refs))
	for _, ref := range refs {
		schema, _ := s.Schema(ref)
		view.Schemas = append(view.Schemas, schema)
	}
	return view
}

func (s *Snapshot) Fingerprint() string {
	if s == nil {
		return ""
	}
	return s.fingerprint
}

// LockDependencies computes the deterministic transitive closure of exact
// extension roots. Provider definitions pull in their operation/primitive
// dependencies, while descriptors pull in options/input/result/event schemas,
// semantic contracts, artifact types and recipient contracts.
func (s *Snapshot) LockDependencies(roots []Ref) (DependencyLock, error) {
	if s == nil {
		return DependencyLock{}, fmt.Errorf("extension snapshot is nil")
	}
	lock := DependencyLock{Version: 1}
	extensionsSeen := map[Ref]bool{}
	schemasSeen := map[Ref]bool{}
	var addRef func(Ref) error
	var addSchema func(Ref) error
	addSchema = func(ref Ref) error {
		if err := ref.Validate(); err != nil || ref.Kind != SchemaKind {
			return fmt.Errorf("dependency %q is not an exact schema reference", ref.Key())
		}
		if schemasSeen[ref] {
			return nil
		}
		schema, ok := s.schemas[ref]
		if !ok {
			return fmt.Errorf("dependency schema %q is unavailable", ref.Key())
		}
		schemasSeen[ref] = true
		lock.Schemas = append(lock.Schemas, SchemaPin{Ref: ref, SHA256: schema.Digest})
		return nil
	}
	addRef = func(ref Ref) error {
		if err := ref.Validate(); err != nil {
			return err
		}
		if ref.Kind == SchemaKind {
			return addSchema(ref)
		}
		if extensionsSeen[ref] {
			return nil
		}
		item, ok := s.registrations[ref]
		if !ok {
			return fmt.Errorf("dependency extension %q is unavailable", ref.Key())
		}
		extensionsSeen[ref] = true
		descriptorBytes, err := json.Marshal(cloneDescriptor(item.descriptor))
		if err != nil {
			return fmt.Errorf("fingerprint extension descriptor %q: %w", ref.Key(), err)
		}
		descriptorDigest := sha256.Sum256(descriptorBytes)
		lock.Extensions = append(lock.Extensions, ExtensionPin{
			Ref: ref, ImplementationVersion: item.descriptor.ImplementationVersion,
			DescriptorSHA256: hex.EncodeToString(descriptorDigest[:]),
		})
		for _, schemaRef := range []*Ref{item.descriptor.OptionsSchemaRef, item.descriptor.InputSchemaRef, item.descriptor.ResultSchemaRef} {
			if schemaRef != nil {
				if err := addSchema(*schemaRef); err != nil {
					return fmt.Errorf("extension %q: %w", ref.Key(), err)
				}
			}
		}
		for _, schemaRef := range item.descriptor.EventSchemaRefs {
			if err := addSchema(schemaRef); err != nil {
				return fmt.Errorf("extension %q: %w", ref.Key(), err)
			}
		}
		dependencies := append([]Ref(nil), item.descriptor.Dependencies...)
		dependencies = append(dependencies, item.descriptor.SemanticContracts...)
		for _, input := range item.descriptor.ArtifactInputs {
			dependencies = append(dependencies, input.TypeRef)
		}
		for _, output := range item.descriptor.ArtifactOutputs {
			dependencies = append(dependencies, output.TypeRef)
		}
		if item.descriptor.ArtifactPolicy != nil {
			dependencies = append(dependencies, item.descriptor.ArtifactPolicy.RecipientContracts...)
		}
		for _, dependency := range dependencies {
			if err := addRef(dependency); err != nil {
				return fmt.Errorf("extension %q dependency: %w", ref.Key(), err)
			}
		}
		return nil
	}

	lock.Roots = append([]Ref(nil), roots...)
	sort.Slice(lock.Roots, func(i, j int) bool { return lock.Roots[i].Key() < lock.Roots[j].Key() })
	for i, root := range lock.Roots {
		if i > 0 && root == lock.Roots[i-1] {
			return DependencyLock{}, fmt.Errorf("duplicate dependency root %q", root.Key())
		}
		if err := addRef(root); err != nil {
			return DependencyLock{}, fmt.Errorf("resolve dependency root %q: %w", root.Key(), err)
		}
	}
	sort.Slice(lock.Extensions, func(i, j int) bool { return lock.Extensions[i].Ref.Key() < lock.Extensions[j].Ref.Key() })
	sort.Slice(lock.Schemas, func(i, j int) bool { return lock.Schemas[i].Ref.Key() < lock.Schemas[j].Ref.Key() })
	return lock, nil
}

// ValidateDependencyLock verifies exact implementation descriptors and schema
// digests, then recomputes the closure so omitted transitive dependencies fail.
func (s *Snapshot) ValidateDependencyLock(lock DependencyLock) error {
	if lock.Version != 1 {
		return fmt.Errorf("unsupported extension dependency lock version %d", lock.Version)
	}
	expected, err := s.LockDependencies(lock.Roots)
	if err != nil {
		return err
	}
	actualJSON, err := json.Marshal(lock)
	if err != nil {
		return err
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(actualJSON, expectedJSON) {
		return fmt.Errorf("extension dependency lock does not match the compiled catalog")
	}
	return nil
}

func (s *Snapshot) Descriptor(ref Ref) (Descriptor, bool) {
	if s == nil {
		return Descriptor{}, false
	}
	item, ok := s.registrations[ref]
	if !ok {
		return Descriptor{}, false
	}
	return cloneDescriptor(item.descriptor), true
}

func (s *Snapshot) Descriptors() []Descriptor {
	if s == nil {
		return nil
	}
	refs := make([]Ref, 0, len(s.registrations))
	for ref := range s.registrations {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key() < refs[j].Key() })
	result := make([]Descriptor, 0, len(refs))
	for _, ref := range refs {
		result = append(result, cloneDescriptor(s.registrations[ref].descriptor))
	}
	return result
}

func (s *Snapshot) Schema(ref Ref) (Schema, bool) {
	if s == nil {
		return Schema{}, false
	}
	item, ok := s.schemas[ref]
	if !ok {
		return Schema{}, false
	}
	item.Document = append(json.RawMessage(nil), item.Document...)
	return item, true
}

// Bind validates options against the frozen schema before constructing a
// typed implementation. A missing options schema means options are forbidden.
func (s *Snapshot) Bind(ref Ref, options json.RawMessage) (Descriptor, any, error) {
	if s == nil {
		return Descriptor{}, nil, fmt.Errorf("extension snapshot is nil")
	}
	item, ok := s.registrations[ref]
	if !ok {
		return Descriptor{}, nil, fmt.Errorf("extension %q is not registered", ref.Key())
	}
	if err := s.ValidateOptions(ref, options); err != nil {
		return Descriptor{}, nil, err
	}
	if len(options) == 0 {
		options = json.RawMessage(`{}`)
	}
	implementation, err := item.factory(append(json.RawMessage(nil), options...))
	if err != nil {
		return Descriptor{}, nil, fmt.Errorf("bind extension %q: %w", ref.Key(), err)
	}
	if implementation == nil {
		return Descriptor{}, nil, fmt.Errorf("extension %q factory returned nil", ref.Key())
	}
	return cloneDescriptor(item.descriptor), implementation, nil
}

// Implementation constructs a catalog-owned, stateless implementation whose
// behavior is configured later by per-binding options. It deliberately does
// not validate options; callers must validate each binding with ValidateOptions
// before execution. Use Bind when options configure the constructed instance.
func (s *Snapshot) Implementation(ref Ref) (Descriptor, any, error) {
	if s == nil {
		return Descriptor{}, nil, fmt.Errorf("extension snapshot is nil")
	}
	item, ok := s.registrations[ref]
	if !ok {
		return Descriptor{}, nil, fmt.Errorf("extension %q is not registered", ref.Key())
	}
	implementation, err := item.factory(json.RawMessage(`{}`))
	if err != nil {
		return Descriptor{}, nil, fmt.Errorf("construct extension %q: %w", ref.Key(), err)
	}
	if implementation == nil {
		return Descriptor{}, nil, fmt.Errorf("extension %q factory returned nil", ref.Key())
	}
	return cloneDescriptor(item.descriptor), implementation, nil
}

func (s *Snapshot) ValidateOptions(ref Ref, options json.RawMessage) error {
	if s == nil {
		return fmt.Errorf("extension snapshot is nil")
	}
	item, ok := s.registrations[ref]
	if !ok {
		return fmt.Errorf("extension %q is not registered", ref.Key())
	}
	if len(options) == 0 {
		options = json.RawMessage(`{}`)
	}
	if item.descriptor.OptionsSchemaRef == nil {
		if !bytes.Equal(bytes.TrimSpace(options), []byte(`{}`)) {
			return fmt.Errorf("extension %q does not accept options", ref.Key())
		}
		return nil
	}
	schemaRef := *item.descriptor.OptionsSchemaRef
	schema, exists := s.compiled[schemaRef]
	if !exists {
		return fmt.Errorf("extension %q options schema %q is unavailable", ref.Key(), schemaRef.Key())
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(options))
	if err != nil {
		return fmt.Errorf("extension %q options are invalid JSON: %w", ref.Key(), err)
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("extension %q options violate schema %q: %w", ref.Key(), schemaRef.Key(), err)
	}
	return nil
}

func (s *Snapshot) ValidateSchema(ref Ref, value json.RawMessage) error {
	if s == nil || ref.Kind != SchemaKind || ref.Validate() != nil {
		return fmt.Errorf("schema validation requires an exact schema reference")
	}
	schema, exists := s.compiled[ref]
	if !exists {
		return fmt.Errorf("schema %q is not registered", ref.Key())
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return fmt.Errorf("value for schema %q is invalid JSON: %w", ref.Key(), err)
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("value violates schema %q: %w", ref.Key(), err)
	}
	return nil
}

// ValidateArtifact resolves the exact artifact contract and enforces its
// ownership, replay, sensitivity, media and size policies before the value or
// body lease may cross a module boundary.
func (s *Snapshot) ValidateArtifact(artifact ArtifactRef, receiver ArtifactOwner, recipient Ref, now time.Time) error {
	if s == nil {
		return fmt.Errorf("artifact catalog is nil")
	}
	descriptor, exists := s.Descriptor(artifact.TypeRef)
	if !exists || artifact.TypeRef.Kind != ArtifactKind || descriptor.ArtifactPolicy == nil {
		return fmt.Errorf("artifact type %q is not registered with an artifact policy", artifact.TypeRef.Key())
	}
	policy := *descriptor.ArtifactPolicy
	if err := artifact.ValidateShape(descriptor.ResourceBounds.MaxInputBytes); err != nil {
		return err
	}
	if !policy.AllowsOwnerDomain(artifact.Owner.Domain) {
		return fmt.Errorf("artifact owner domain %q is not allowed by %q", artifact.Owner.Domain, artifact.TypeRef.Key())
	}
	if !policy.AllowsScope(artifact.ReplayScope) {
		return fmt.Errorf("artifact replay scope %q is not allowed by %q", artifact.ReplayScope, artifact.TypeRef.Key())
	}
	if sensitivityRank(artifact.Sensitivity) > sensitivityRank(policy.SensitivityLimit) {
		return fmt.Errorf("artifact sensitivity %q exceeds the registered limit", artifact.Sensitivity)
	}
	if !policy.AllowsMediaType(artifact.MediaType) {
		return fmt.Errorf("artifact media type %q is not allowed", artifact.MediaType)
	}
	if now.IsZero() {
		now = time.Now()
	}
	if !artifact.ExpiresAt.IsZero() && !artifact.ExpiresAt.After(now) {
		return fmt.Errorf("artifact has expired")
	}
	if artifact.Body != nil && !artifact.Body.ExpiresAt.IsZero() && !artifact.Body.ExpiresAt.After(now) {
		return fmt.Errorf("artifact body lease has expired")
	}
	if len(artifact.Value) > 0 {
		if descriptor.InputSchemaRef == nil {
			return fmt.Errorf("artifact type %q does not declare an inline value schema", artifact.TypeRef.Key())
		}
		if err := s.ValidateSchema(*descriptor.InputSchemaRef, artifact.Value); err != nil {
			return fmt.Errorf("artifact inline value: %w", err)
		}
		if artifact.SHA256 != "" {
			digest := sha256.Sum256(artifact.Value)
			if hex.EncodeToString(digest[:]) != artifact.SHA256 {
				return fmt.Errorf("artifact inline digest mismatch")
			}
		}
	}
	if !artifactOwnersEqual(artifact.Owner, receiver) {
		if !containsRef(policy.RecipientContracts, recipient) {
			return fmt.Errorf("artifact owned by %q cannot be transferred to this recipient", artifact.Owner.Domain)
		}
		switch artifact.ReplayScope {
		case ArtifactReplayRequest:
			// The exact recipient is authorized for this invocation only.
		case ArtifactReplaySession:
			if artifact.Owner.SessionID == "" || artifact.Owner.SessionID != receiver.SessionID {
				return fmt.Errorf("session-scoped artifact cannot cross session owners")
			}
		case ArtifactReplayConnection:
			if artifact.Owner.ConnectionID == "" || artifact.Owner.ConnectionID != receiver.ConnectionID {
				return fmt.Errorf("connection-scoped artifact cannot cross connection owners")
			}
		case ArtifactReplayProvider:
			if artifact.Owner.ProviderDefinitionID == "" || artifact.Owner.ProviderDefinitionID != receiver.ProviderDefinitionID {
				return fmt.Errorf("provider-scoped artifact cannot cross provider owners")
			}
		case ArtifactReplayPortable:
			if artifact.Sensitivity == ArtifactSecret {
				return fmt.Errorf("secret artifacts cannot be transferred portably")
			}
		default:
			return fmt.Errorf("artifact replay scope %q cannot cross owners", artifact.ReplayScope)
		}
	}
	return nil
}

func (c *Catalog) validateGraph() error {
	for _, item := range c.registrations {
		descriptor := item.descriptor
		if descriptor.OptionsSchemaRef != nil {
			if _, ok := c.schemas[*descriptor.OptionsSchemaRef]; !ok {
				return fmt.Errorf("extension %q references missing options schema %q", descriptor.Ref.Key(), descriptor.OptionsSchemaRef.Key())
			}
		}
		payloadSchemas := append([]Ref(nil), descriptor.EventSchemaRefs...)
		for _, schemaRef := range []*Ref{descriptor.InputSchemaRef, descriptor.ResultSchemaRef} {
			if schemaRef != nil {
				payloadSchemas = append(payloadSchemas, *schemaRef)
			}
		}
		for _, schemaRef := range payloadSchemas {
			if schemaRef.Kind != SchemaKind {
				return fmt.Errorf("extension %q payload schema %q must use kind %q", descriptor.Ref.Key(), schemaRef.Key(), SchemaKind)
			}
			if _, ok := c.schemas[schemaRef]; !ok {
				return fmt.Errorf("extension %q references missing schema %q", descriptor.Ref.Key(), schemaRef.Key())
			}
		}
		for _, dependency := range descriptor.Dependencies {
			if _, ok := c.registrations[dependency]; !ok {
				return fmt.Errorf("extension %q references missing dependency %q", descriptor.Ref.Key(), dependency.Key())
			}
		}
		for _, semantic := range descriptor.SemanticContracts {
			if _, ok := c.registrations[semantic]; !ok {
				return fmt.Errorf("extension %q references missing semantic contract %q", descriptor.Ref.Key(), semantic.Key())
			}
		}
		for _, input := range descriptor.ArtifactInputs {
			artifact, ok := c.registrations[input.TypeRef]
			if !ok || artifact.descriptor.ArtifactPolicy == nil {
				return fmt.Errorf("operation %q artifact input %q references missing artifact contract %q", descriptor.Ref.Key(), input.Role, input.TypeRef.Key())
			}
		}
		for _, output := range descriptor.ArtifactOutputs {
			artifact, ok := c.registrations[output.TypeRef]
			if !ok || artifact.descriptor.ArtifactPolicy == nil {
				return fmt.Errorf("operation %q artifact output %q references missing artifact contract %q", descriptor.Ref.Key(), output.Role, output.TypeRef.Key())
			}
		}
		if descriptor.ArtifactPolicy != nil {
			for _, recipient := range descriptor.ArtifactPolicy.RecipientContracts {
				if _, ok := c.registrations[recipient]; !ok {
					return fmt.Errorf("artifact %q references missing recipient contract %q", descriptor.Ref.Key(), recipient.Key())
				}
			}
		}
	}
	state := map[Ref]uint8{}
	var visit func(Ref) error
	visit = func(ref Ref) error {
		switch state[ref] {
		case 1:
			return fmt.Errorf("extension dependency cycle at %q", ref.Key())
		case 2:
			return nil
		}
		state[ref] = 1
		for _, dependency := range c.registrations[ref].descriptor.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[ref] = 2
		return nil
	}
	refs := make([]Ref, 0, len(c.registrations))
	for ref := range c.registrations {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key() < refs[j].Key() })
	for _, ref := range refs {
		if err := visit(ref); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) compileSchemas() (map[Ref]*jsonschema.Schema, error) {
	refs := make([]Ref, 0, len(c.schemas))
	for ref := range c.schemas {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key() < refs[j].Key() })
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	resources := map[string]any{}
	for _, ref := range refs {
		resource, err := jsonschema.UnmarshalJSON(bytes.NewReader(c.schemas[ref].Document))
		if err != nil {
			return nil, fmt.Errorf("decode schema %q: %w", ref.Key(), err)
		}
		location := schemaURI(ref)
		if err := compiler.AddResource(location, resource); err != nil {
			return nil, fmt.Errorf("register schema %q: %w", ref.Key(), err)
		}
		resources[location] = resource
	}
	compiler.UseLoader(jsonschema.SchemeURLLoader{"https": localSchemaLoader{resources: resources}})
	compiled := make(map[Ref]*jsonschema.Schema, len(refs))
	for _, ref := range refs {
		schema, err := compiler.Compile(schemaURI(ref))
		if err != nil {
			return nil, fmt.Errorf("compile schema %q: %w", ref.Key(), err)
		}
		compiled[ref] = schema
	}
	return compiled, nil
}

type localSchemaLoader struct{ resources map[string]any }

func (l localSchemaLoader) Load(rawURL string) (any, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host != schemaHost || u.User != nil {
		return nil, fmt.Errorf("remote or untrusted schema reference %q is disabled", rawURL)
	}
	resource, ok := l.resources[u.String()]
	if !ok {
		return nil, fmt.Errorf("schema resource %q is not registered", rawURL)
	}
	return resource, nil
}

func schemaURI(ref Ref) string {
	kind := base64.RawURLEncoding.EncodeToString([]byte(ref.Kind))
	id := base64.RawURLEncoding.EncodeToString([]byte(ref.ID))
	return fmt.Sprintf("https://%s/%s/%s/%d", schemaHost, kind, id, ref.ContractVersion)
}

func (c *Catalog) fingerprint() string {
	schemaRefs := make([]Ref, 0, len(c.schemas))
	for ref := range c.schemas {
		schemaRefs = append(schemaRefs, ref)
	}
	sort.Slice(schemaRefs, func(i, j int) bool { return schemaRefs[i].Key() < schemaRefs[j].Key() })
	refs := make([]Ref, 0, len(c.registrations))
	for ref := range c.registrations {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key() < refs[j].Key() })
	hash := sha256.New()
	for _, ref := range schemaRefs {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", ref.Key(), c.schemas[ref].Digest)
	}
	for _, ref := range refs {
		item := c.registrations[ref].descriptor
		canonical, err := json.Marshal(item)
		if err == nil {
			_, _ = hash.Write(canonical)
		}
		_, _ = fmt.Fprint(hash, "\x00")
		if item.OptionsSchemaRef != nil {
			_, _ = io.WriteString(hash, c.schemas[*item.OptionsSchemaRef].Digest)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, nil, fmt.Errorf("multiple JSON values are not allowed")
		}
		return nil, nil, err
	}
	canonical, err := json.Marshal(value)
	return canonical, value, err
}

func validateBounds(descriptor Descriptor) error {
	for name, value := range map[string]int64{
		"maxInputBytes":    descriptor.ResourceBounds.MaxInputBytes,
		"maxOutputBytes":   descriptor.ResourceBounds.MaxOutputBytes,
		"maxBufferedBytes": descriptor.ResourceBounds.MaxBufferedBytes,
		"deadlineMillis":   descriptor.ResourceBounds.DeadlineMillis,
	} {
		if value < 0 {
			return fmt.Errorf("resource bound %s cannot be negative", name)
		}
	}
	if descriptor.Ref.Kind == "transform" && (descriptor.ResourceBounds.MaxBufferedBytes <= 0 || descriptor.ResourceBounds.DeadlineMillis <= 0) {
		return fmt.Errorf("transform requires positive maxBufferedBytes and deadlineMillis")
	}
	return nil
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Dependencies = append([]Ref(nil), descriptor.Dependencies...)
	descriptor.SemanticContracts = append([]Ref(nil), descriptor.SemanticContracts...)
	descriptor.ArtifactInputs = append([]ArtifactInput(nil), descriptor.ArtifactInputs...)
	descriptor.ArtifactOutputs = append([]ArtifactOutput(nil), descriptor.ArtifactOutputs...)
	descriptor.EventSchemaRefs = append([]Ref(nil), descriptor.EventSchemaRefs...)
	descriptor.LifecycleCapabilities = append([]string(nil), descriptor.LifecycleCapabilities...)
	descriptor.FailureModes = append([]string(nil), descriptor.FailureModes...)
	descriptor.SetupView = append(json.RawMessage(nil), descriptor.SetupView...)
	if descriptor.OptionsSchemaRef != nil {
		copy := *descriptor.OptionsSchemaRef
		descriptor.OptionsSchemaRef = &copy
	}
	if descriptor.InputSchemaRef != nil {
		copy := *descriptor.InputSchemaRef
		descriptor.InputSchemaRef = &copy
	}
	if descriptor.ResultSchemaRef != nil {
		copy := *descriptor.ResultSchemaRef
		descriptor.ResultSchemaRef = &copy
	}
	if descriptor.ArtifactPolicy != nil {
		policy := cloneArtifactPolicy(*descriptor.ArtifactPolicy)
		descriptor.ArtifactPolicy = &policy
	}
	return descriptor
}
