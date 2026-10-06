package provider

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/fm39hz/gobroom/internal/extensions"
)

var (
	endpointOptionsSchemaRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.endpoint-options", ContractVersion: 1}
	usageOptionsSchemaRef    = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.usage-options", ContractVersion: 1}
	errorOptionsSchemaRef    = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.http-json-error-options", ContractVersion: 1}
	oauthOptionsSchemaRef    = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.auth.oauth2-options", ContractVersion: 1}
)

const schemaDraft2020 = "https://json-schema.org/draft/2020-12/schema"

func bindSchemaDocument(ref extensions.Ref, shape json.RawMessage) (json.RawMessage, error) {
	uri, err := ref.URI()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(shape))
	decoder.DisallowUnknownFields()
	var document map[string]json.RawMessage
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode schema body: %w", err)
	}
	if document == nil {
		return nil, fmt.Errorf("schema body must be a JSON object")
	}
	draft, _ := json.Marshal(schemaDraft2020)
	id, _ := json.Marshal(uri)
	document["$schema"] = draft
	document["$id"] = id
	return json.Marshal(document)
}

func endpointOptionsSchema() (json.RawMessage, error) {
	return bindSchemaDocument(endpointOptionsSchemaRef, json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","minLength":1},
  "query":{"type":"object","additionalProperties":{"type":"string"}}
},
"additionalProperties":false
}`))
}

func usageOptionsSchema() (json.RawMessage, error) {
	return bindSchemaDocument(usageOptionsSchemaRef, json.RawMessage(`{
"type":"object",
"properties":{
  "inputTokensHeader":{"type":"string","minLength":1},
  "outputTokensHeader":{"type":"string","minLength":1},
  "estimatedCostHeader":{"type":"string","minLength":1}
},
"additionalProperties":false
}`))
}

func httpJSONErrorOptionsSchema() (json.RawMessage, error) {
	return bindSchemaDocument(errorOptionsSchemaRef, json.RawMessage(`{
"type":"object",
"properties":{"httpJson":{
  "type":"object",
  "properties":{
    "defaultScope":{"enum":["request","route","route_connection","connection","provider"]},
    "authScope":{"enum":["request","route","route_connection","connection","provider"]},
    "quotaScope":{"enum":["request","route","route_connection","connection","provider"]},
    "rateLimitScope":{"enum":["request","route","route_connection","connection","provider"]},
    "capacityScope":{"enum":["request","route","route_connection","connection","provider"]},
    "codePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "typePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "statusPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "messagePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "resetAtPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "retryAfterPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "retryDelayPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "windowNamePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "windowLimitPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "windowUsedPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "windowRemainingPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "quotaWindowName":{"type":"string"},
    "quotaWindowKind":{"type":"string"},
    "quotaCodes":{"type":"array","items":{"type":"string","minLength":1}},
    "quotaTypes":{"type":"array","items":{"type":"string","minLength":1}},
    "quotaStatuses":{"type":"array","items":{"type":"string","minLength":1}},
    "quotaMessageTokens":{"type":"array","items":{"type":"string","minLength":1}}
  },
  "additionalProperties":false
}},
"additionalProperties":false
}`))
}

func oauthOptionsSchema() (json.RawMessage, error) {
	return bindSchemaDocument(oauthOptionsSchemaRef, json.RawMessage(`{
"type":"object",
"properties":{
  "oauth":{
    "type":"object",
    "properties":{
      "clientId":{"type":"string","minLength":1},
      "authUrl":{"type":"string","format":"uri"},
      "tokenUrl":{"type":"string","format":"uri"},
      "scopes":{"type":"array","items":{"type":"string","minLength":1}},
      "redirectUrl":{"type":"string","format":"uri"}
    },
    "required":["clientId","authUrl","tokenUrl"],
    "additionalProperties":false
  },
  "options":false
},
"required":["oauth"],
"additionalProperties":false
}`))
}
