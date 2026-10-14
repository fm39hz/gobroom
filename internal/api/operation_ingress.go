package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/provider"
)

// These API names remain aliases for the shared extension-owned contracts.
type OperationIngress = provider.ClientIngress
type IngressRoute = provider.ClientIngressRoute
type JSONOperationIngress = provider.JSONOperationIngress

// MultipartArtifactBinding maps one declared form field to an exact artifact
// contract. Role identifies the semantic use of that artifact within an
// operation; it is not an upstream/provider name.
type MultipartArtifactBinding struct {
	Field    string         `json:"field"`
	Role     string         `json:"role"`
	TypeRef  extensions.Ref `json:"typeRef"`
	MaxBytes int64          `json:"maxBytes"`
	TTL      time.Duration  `json:"ttl,omitempty"`
}

// MultipartOperationIngress is a data-driven binary ingress. The "request"
// part contains the same JSON envelope accepted by JSONOperationIngress;
// declared file parts become bounded BodyRef artifacts rather than in-memory
// byte slices.
type MultipartOperationIngress struct {
	CodecID                  string
	IngressContractVersion   uint64
	Operation                normalize.Operation
	OperationContractVersion uint64
	Endpoints                []IngressRoute
	Bindings                 []MultipartArtifactBinding
	RequestField             string
}

func (c MultipartOperationIngress) ID() string { return c.CodecID }
func (c MultipartOperationIngress) IngressRef() extensions.Ref {
	return extensions.Ref{Kind: extensions.ClientIngressKind, ID: c.CodecID, ContractVersion: c.IngressContractVersion}
}
func (c MultipartOperationIngress) OperationRef() extensions.Ref {
	return extensions.Ref{Kind: "operation", ID: string(c.Operation), ContractVersion: c.OperationContractVersion}
}
func (c MultipartOperationIngress) Routes() []IngressRoute {
	return append([]IngressRoute(nil), c.Endpoints...)
}
func (c MultipartOperationIngress) requestField() string {
	if strings.TrimSpace(c.RequestField) == "" {
		return "request"
	}
	return c.RequestField
}

func (c MultipartOperationIngress) Decode(request *http.Request) (normalize.Request, error) {
	if request == nil || request.Body == nil {
		return normalize.Request{}, fmt.Errorf("multipart request body is required")
	}
	store, ok := artifacts.FromContext(request.Context())
	if !ok {
		return normalize.Request{}, ErrArtifactIngressUnavailable
	}
	multipartReader, err := request.MultipartReader()
	if err != nil {
		return normalize.Request{}, fmt.Errorf("read multipart operation body: %w", err)
	}
	requestField := c.requestField()
	bindings := make(map[string]MultipartArtifactBinding, len(c.Bindings))
	for _, binding := range c.Bindings {
		if strings.TrimSpace(binding.Field) == "" || strings.TrimSpace(binding.Role) == "" || binding.MaxBytes <= 0 {
			return normalize.Request{}, fmt.Errorf("multipart artifact binding requires field, role and positive maxBytes")
		}
		if err := binding.TypeRef.Validate(); err != nil || binding.TypeRef.Kind != extensions.ArtifactKind {
			return normalize.Request{}, fmt.Errorf("multipart binding %q requires an exact artifact type reference", binding.Field)
		}
		if binding.Field == requestField {
			return normalize.Request{}, fmt.Errorf("multipart request field %q conflicts with an artifact binding", requestField)
		}
		if _, duplicate := bindings[binding.Field]; duplicate {
			return normalize.Request{}, fmt.Errorf("multipart field %q has duplicate artifact bindings", binding.Field)
		}
		bindings[binding.Field] = binding
	}

	var requestJSON []byte
	seenFields := make(map[string]bool, len(bindings)+1)
	result := normalize.Request{}
	created := make([]extensions.ArtifactRef, 0, len(bindings))
	complete := false
	defer func() {
		if complete {
			return
		}
		for _, artifact := range created {
			if artifact.Body != nil {
				_ = store.Release(artifact.Owner, *artifact.Body)
			}
		}
	}()
	for {
		part, nextErr := multipartReader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return normalize.Request{}, fmt.Errorf("read multipart operation part: %w", nextErr)
		}
		field := part.FormName()
		if seenFields[field] {
			_ = part.Close()
			return normalize.Request{}, fmt.Errorf("multipart field %q must not be repeated", field)
		}
		seenFields[field] = true
		if field == requestField {
			requestJSON, err = io.ReadAll(io.LimitReader(part, maxIngressBodyBytes+1))
			_ = part.Close()
			if err != nil {
				return normalize.Request{}, fmt.Errorf("read multipart request metadata: %w", err)
			}
			if len(requestJSON) > maxIngressBodyBytes {
				return normalize.Request{}, &http.MaxBytesError{Limit: maxIngressBodyBytes}
			}
			continue
		}
		binding, exists := bindings[field]
		if !exists || part.FileName() == "" {
			_ = part.Close()
			return normalize.Request{}, fmt.Errorf("multipart part %q is not a declared file artifact", field)
		}
		mediaType, _, parseErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if parseErr != nil || mediaType == "" {
			mediaType = "application/octet-stream"
		}
		owner := extensions.ArtifactOwner{Domain: "client", ClientContract: c.CodecID}
		bodyRef, putErr := store.Put(request.Context(), owner, mediaType, part, binding.MaxBytes, binding.TTL, true)
		_ = part.Close()
		if putErr != nil {
			return normalize.Request{}, fmt.Errorf("spool multipart artifact %q: %w", field, putErr)
		}
		artifact := extensions.ArtifactRef{
			TypeRef: binding.TypeRef, Role: binding.Role, Owner: owner,
			ReplayScope: extensions.ArtifactReplayRequest, Sensitivity: extensions.ArtifactPrivate,
			MediaType: bodyRef.MediaType, SizeBytes: bodyRef.SizeBytes, SHA256: bodyRef.SHA256,
			ExpiresAt: bodyRef.ExpiresAt, Body: &bodyRef,
		}
		created = append(created, artifact)
		result.Artifacts = append(result.Artifacts, artifact)
	}
	if len(requestJSON) == 0 {
		return normalize.Request{}, fmt.Errorf("multipart request field %q is required", requestField)
	}
	decoded, err := (JSONOperationIngress{CodecID: c.CodecID, Operation: string(c.Operation), OperationContractVersion: c.OperationContractVersion}).DecodeBody(request, requestJSON)
	if err != nil {
		return normalize.Request{}, err
	}
	decoded.Artifacts = append(decoded.Artifacts, result.Artifacts...)
	complete = true
	return decoded, nil
}

var ErrArtifactIngressUnavailable = errors.New("artifact body store is unavailable")

func validateMultipartOperationBindings(codec MultipartOperationIngress, snapshot *operations.Snapshot) error {
	if snapshot == nil {
		return fmt.Errorf("operation snapshot is unavailable")
	}
	inputs, ok := snapshot.ArtifactInputs(codec.OperationRef())
	if !ok {
		return fmt.Errorf("operation %q is not registered", codec.OperationRef().Key())
	}
	declared := make(map[string]operations.ArtifactInput, len(inputs))
	for _, input := range inputs {
		declared[input.Role] = input
	}
	catalog := snapshot.ExtensionCatalog()
	bindingCounts := map[string]int{}
	for _, binding := range codec.Bindings {
		input, exists := declared[binding.Role]
		if !exists || input.TypeRef != binding.TypeRef {
			return fmt.Errorf("multipart ingress %q binding %q/%q is not a declared input of operation %q", codec.CodecID, binding.Role, binding.TypeRef.Key(), codec.OperationRef().Key())
		}
		descriptor, exists := catalog.Descriptor(binding.TypeRef)
		if !exists || descriptor.ResourceBounds.MaxInputBytes <= 0 || binding.MaxBytes > descriptor.ResourceBounds.MaxInputBytes {
			return fmt.Errorf("multipart ingress %q binding %q exceeds the artifact contract byte limit", codec.CodecID, binding.Role)
		}
		bindingCounts[binding.Role]++
	}
	for _, input := range inputs {
		if bindingCounts[input.Role] < input.MinCount || bindingCounts[input.Role] > input.MaxCount {
			return fmt.Errorf("multipart ingress %q binds %d field(s) to role %q, operation requires %d..%d", codec.CodecID, bindingCounts[input.Role], input.Role, input.MinCount, input.MaxCount)
		}
	}
	return nil
}

func validateOperationIngress(codec OperationIngress) error {
	if err := provider.ValidateClientIngress(codec); err != nil {
		return err
	}
	if multipartCodec, ok := codec.(MultipartOperationIngress); ok {
		if multipartCodec.IngressContractVersion == 0 || multipartCodec.OperationContractVersion == 0 || len(multipartCodec.Bindings) == 0 {
			return fmt.Errorf("multipart operation ingress %q requires exact ingress/operation versions and artifact bindings", codec.ID())
		}
		fieldNames := map[string]bool{}
		for _, binding := range multipartCodec.Bindings {
			if strings.TrimSpace(binding.Field) == "" || strings.TrimSpace(binding.Role) == "" || binding.MaxBytes <= 0 {
				return fmt.Errorf("multipart ingress %q has an incomplete artifact binding", codec.ID())
			}
			if err := binding.TypeRef.Validate(); err != nil || binding.TypeRef.Kind != extensions.ArtifactKind {
				return fmt.Errorf("multipart ingress %q binding %q requires an exact artifact reference", codec.ID(), binding.Field)
			}
			if fieldNames[binding.Field] || binding.Field == multipartCodec.requestField() {
				return fmt.Errorf("multipart ingress %q repeats or conflicts with a request field", codec.ID())
			}
			fieldNames[binding.Field] = true
		}
	}
	return nil
}
