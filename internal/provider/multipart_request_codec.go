package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path"
	"strings"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

const MultipartRequestCodecID = "multipart-form"

type MultipartArtifactField struct {
	Role     string `json:"role"`
	Field    string `json:"field"`
	FileName string `json:"fileName,omitempty"`
}

type MultipartPayloadField struct {
	Property string `json:"property"`
	Field    string `json:"field"`
	Required bool   `json:"required,omitempty"`
}

type MultipartRequestCodecOptions struct {
	ModelField    string                   `json:"modelField"`
	PayloadField  string                   `json:"payloadField,omitempty"`
	PayloadFields []MultipartPayloadField  `json:"payloadFields,omitempty"`
	StreamField   string                   `json:"streamField,omitempty"`
	Artifacts     []MultipartArtifactField `json:"artifacts"`
}

type MultipartRequestCodec struct {
	options        MultipartRequestCodecOptions
	artifactFields map[string]MultipartArtifactField
}

func NewMultipartRequestCodec(options MultipartRequestCodecOptions) (*MultipartRequestCodec, error) {
	options.ModelField = strings.TrimSpace(options.ModelField)
	options.PayloadField = strings.TrimSpace(options.PayloadField)
	options.StreamField = strings.TrimSpace(options.StreamField)
	if options.ModelField == "" || ((options.PayloadField == "") == (len(options.PayloadFields) == 0)) {
		return nil, fmt.Errorf("multipart codec requires modelField and exactly one payload mapping mode")
	}
	for _, field := range []string{options.ModelField, options.PayloadField, options.StreamField} {
		if field != "" && mime.FormatMediaType("form-data", map[string]string{"name": field}) == "" {
			return nil, fmt.Errorf("multipart form field %q cannot be encoded", field)
		}
	}
	fields := map[string]bool{options.ModelField: true}
	if options.PayloadField != "" {
		if fields[options.PayloadField] {
			return nil, fmt.Errorf("multipart codec field %q is assigned more than once", options.PayloadField)
		}
		fields[options.PayloadField] = true
	}
	if options.StreamField != "" {
		if fields[options.StreamField] {
			return nil, fmt.Errorf("multipart codec field %q is assigned more than once", options.StreamField)
		}
		fields[options.StreamField] = true
	}
	seenProperties := map[string]bool{}
	for index := range options.PayloadFields {
		binding := options.PayloadFields[index]
		binding.Property = strings.TrimSpace(binding.Property)
		binding.Field = strings.TrimSpace(binding.Field)
		if binding.Property == "" || binding.Field == "" || seenProperties[binding.Property] || fields[binding.Field] {
			return nil, fmt.Errorf("multipart payload-field mapping %d is empty or duplicated", index)
		}
		if mime.FormatMediaType("form-data", map[string]string{"name": binding.Field}) == "" {
			return nil, fmt.Errorf("multipart payload form field %q cannot be encoded", binding.Field)
		}
		seenProperties[binding.Property] = true
		fields[binding.Field] = true
		options.PayloadFields[index] = binding
	}
	artifactFields := make(map[string]MultipartArtifactField, len(options.Artifacts))
	for index, binding := range options.Artifacts {
		binding.Role = strings.TrimSpace(binding.Role)
		binding.Field = strings.TrimSpace(binding.Field)
		binding.FileName = strings.TrimSpace(binding.FileName)
		if binding.Role == "" || binding.Field == "" {
			return nil, fmt.Errorf("multipart artifact mapping %d requires role and field", index)
		}
		if _, duplicate := artifactFields[binding.Role]; duplicate {
			return nil, fmt.Errorf("multipart artifact role %q is mapped more than once", binding.Role)
		}
		if fields[binding.Field] {
			return nil, fmt.Errorf("multipart field %q is assigned more than once", binding.Field)
		}
		if formatted := mime.FormatMediaType("form-data", map[string]string{"name": binding.Field, "filename": defaultMultipartFileName(binding)}); formatted == "" {
			return nil, fmt.Errorf("multipart field %q cannot be encoded", binding.Field)
		}
		fields[binding.Field] = true
		artifactFields[binding.Role] = binding
	}
	options.Artifacts = append([]MultipartArtifactField(nil), options.Artifacts...)
	options.PayloadFields = append([]MultipartPayloadField(nil), options.PayloadFields...)
	return &MultipartRequestCodec{options: options, artifactFields: artifactFields}, nil
}

func (c *MultipartRequestCodec) ID() string { return MultipartRequestCodecID }

func (c *MultipartRequestCodec) ArtifactRoleMappings() map[string]string {
	result := make(map[string]string, len(c.artifactFields))
	for role, binding := range c.artifactFields {
		result[role] = binding.Field
	}
	return result
}

func (c *MultipartRequestCodec) DescribeCompatibility(input kernel.CompatibilityContext) []kernel.FacetMapping {
	result := make([]kernel.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		mapping := kernel.FacetMapping{Facet: facet, Paths: []string{"multipart.form"}, Disposition: kernel.FacetUnsupported, Reason: "multipart form codec only maps the operation payload and declared artifact ports"}
		switch facet {
		case kernel.FacetWireRequest:
			mapping.Disposition = kernel.FacetTranslated
			mapping.Reason = "model, operation payload and declared artifact ports are encoded as multipart form fields"
		}
		if kernel.IsOperationArtifactFacet(facet) {
			role := strings.TrimPrefix(facet, kernel.FacetOperationArtifactPrefix)
			if _, exists := c.artifactFields[role]; exists {
				mapping.Disposition = kernel.FacetTranslated
				mapping.Reason = "the named operation artifact is streamed into its declared multipart field"
			}
		}
		result = append(result, mapping)
	}
	return result
}

func (c *MultipartRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, _ kernel.Route, _ kernel.Credential) (kernel.UpstreamRequest, error) {
	if c == nil || ctx == nil || request.Model == "" {
		return kernel.UpstreamRequest{}, fmt.Errorf("multipart codec requires a context and model")
	}
	if err := ctx.Err(); err != nil {
		return kernel.UpstreamRequest{}, err
	}
	if request.Stream && c.options.StreamField == "" {
		return kernel.UpstreamRequest{}, fmt.Errorf("multipart codec does not declare a stream field for streaming requests")
	}
	payload := request.OperationPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if _, err := multipartPayloadValues(c.options, payload); err != nil {
		return kernel.UpstreamRequest{}, err
	}
	for _, artifact := range request.Artifacts {
		if _, ok := c.artifactFields[artifact.Role]; !ok {
			return kernel.UpstreamRequest{}, fmt.Errorf("multipart codec has no field mapping for artifact role %q", artifact.Role)
		}
	}
	leases := make([]*artifacts.Lease, 0, len(request.Artifacts))
	for _, artifact := range request.Artifacts {
		lease, err := artifacts.OpenArtifactFromContext(ctx, artifact)
		if err != nil {
			for _, opened := range leases {
				_ = opened.Close()
			}
			return kernel.UpstreamRequest{}, fmt.Errorf("open artifact role %q: %w", artifact.Role, err)
		}
		leases = append(leases, lease)
	}

	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	headers := make(http.Header)
	headers.Set("Content-Type", form.FormDataContentType())
	stopCancellation := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	go func() {
		err := writeMultipartOperation(ctx, form, request, c.options, c.artifactFields, leases)
		stopCancellation()
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_ = writer.Close()
	}()
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: "/", Headers: headers, Body: reader}, nil
}

func writeMultipartOperation(ctx context.Context, form *multipart.Writer, request kernel.NormalizedRequest, options MultipartRequestCodecOptions, mappings map[string]MultipartArtifactField, leases []*artifacts.Lease) error {
	defer func() {
		for _, lease := range leases {
			_ = lease.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := form.WriteField(options.ModelField, request.Model); err != nil {
		return err
	}
	payload := request.OperationPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if options.PayloadField != "" {
		if err := form.WriteField(options.PayloadField, string(payload)); err != nil {
			return err
		}
	} else {
		values, err := multipartPayloadValues(options, payload)
		if err != nil {
			return err
		}
		for _, binding := range options.PayloadFields {
			value, exists := values[binding.Property]
			if !exists {
				continue
			}
			var textValue string
			if err := json.Unmarshal(value, &textValue); err != nil {
				textValue = string(value)
			}
			if err := form.WriteField(binding.Field, textValue); err != nil {
				return err
			}
		}
	}
	if options.StreamField != "" {
		if err := form.WriteField(options.StreamField, fmt.Sprintf("%t", request.Stream)); err != nil {
			return err
		}
	}
	for index, artifact := range request.Artifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		mapping := mappings[artifact.Role]
		filename := defaultMultipartFileName(mapping)
		disposition := mime.FormatMediaType("form-data", map[string]string{"name": mapping.Field, "filename": filename})
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", disposition)
		header.Set("Content-Type", artifact.MediaType)
		part, err := form.CreatePart(header)
		if err != nil {
			return err
		}
		if _, err := io.Copy(part, leases[index]); err != nil {
			return fmt.Errorf("write multipart artifact role %q: %w", artifact.Role, err)
		}
	}
	return form.Close()
}

func multipartPayloadValues(options MultipartRequestCodecOptions, payload json.RawMessage) (map[string]json.RawMessage, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("operation payload is not valid JSON")
	}
	if options.PayloadField != "" {
		return nil, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil || values == nil {
		return nil, fmt.Errorf("operation payload must be a JSON object when payloadFields are configured")
	}
	mapped := make(map[string]bool, len(options.PayloadFields))
	for _, binding := range options.PayloadFields {
		if _, exists := values[binding.Property]; !exists && binding.Required {
			return nil, fmt.Errorf("operation payload is missing mapped property %q", binding.Property)
		}
		mapped[binding.Property] = true
	}
	for property := range values {
		if !mapped[property] {
			return nil, fmt.Errorf("operation payload property %q has no multipart field mapping", property)
		}
	}
	return values, nil
}

func defaultMultipartFileName(binding MultipartArtifactField) string {
	if binding.FileName != "" {
		return binding.FileName
	}
	name := path.Base(binding.Role)
	if name == "." || name == "/" || name == "" {
		name = "artifact"
	}
	return name + ".bin"
}

func multipartRequestCodecFactory(raw json.RawMessage) (any, error) {
	var options MultipartRequestCodecOptions
	if err := decodeStrictJSON(raw, &options); err != nil {
		return nil, fmt.Errorf("decode multipart request codec options: %w", err)
	}
	return NewMultipartRequestCodec(options)
}

func registerMultipartRequestCodec(registry *RuntimeRegistry) error {
	optionsSchemaRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.request-codec.multipart-form.options", ContractVersion: 1}
	optionsSchema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"modelField":{"type":"string","minLength":1},
			"payloadField":{"type":"string","minLength":1},
			"payloadFields":{"type":"array","minItems":1,"items":{"type":"object","properties":{"property":{"type":"string","minLength":1},"field":{"type":"string","minLength":1},"required":{"type":"boolean"}},"required":["property","field"],"additionalProperties":false}},
			"streamField":{"type":"string","minLength":1},
			"artifacts":{"type":"array","minItems":1,"items":{"type":"object","properties":{"role":{"type":"string","minLength":1},"field":{"type":"string","minLength":1},"fileName":{"type":"string","minLength":1}},"required":["role","field"],"additionalProperties":false}}
		},
		"required":["modelField"],
		"oneOf":[{"required":["payloadField"]},{"required":["payloadFields"]}],
		"additionalProperties":false
	}`)
	return registry.RegisterConfiguredRequestCodec(MultipartRequestCodecID, 1, optionsSchemaRef, optionsSchema, multipartRequestCodecFactory)
}
