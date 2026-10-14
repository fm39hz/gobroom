package provider

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/fm39hz/gobroom/internal/extensions"
)

var (
	endpointOptionsSchemaRef    = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.endpoint-options", ContractVersion: 1}
	staticModelSourceSchemaRef  = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.static-model-source-options", ContractVersion: 1}
	usageOptionsSchemaRef       = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.usage-options", ContractVersion: 1}
	quotaSourceOptionsSchemaRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.http-json-quota-options", ContractVersion: 1}
	errorOptionsSchemaRef       = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.provider.http-json-error-options", ContractVersion: 1}
	oauthOptionsSchemaRef       = extensions.Ref{Kind: extensions.SchemaKind, ID: "gobroom.auth.oauth2-options", ContractVersion: 1}
)

func staticModelSourceOptionsSchema() (json.RawMessage, error) {
	return bindSchemaDocument(staticModelSourceSchemaRef, json.RawMessage(`{
"type":"object",
"required":["models"],
"properties":{"models":{"type":"array","minItems":1,"maxItems":2048,"items":{
  "type":"object","required":["id"],"properties":{
    "id":{"type":"string","minLength":1,"maxLength":512},
    "displayName":{"type":"string","maxLength":512},
    "profile":{"type":"object","maxProperties":128,"additionalProperties":{"type":"object","properties":{
      "state":{"enum":["unknown","unsupported","native","emulated","conditional"]},
      "formats":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1}},
      "maxItems":{"type":"integer","minimum":0},
      "constraints":{"type":"object"}
    },"required":["state"],"additionalProperties":false}},
    "metadata":{"type":"object","maxProperties":128,"additionalProperties":true}
  },"additionalProperties":false
}}},
"additionalProperties":false
}`))
}

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

func quotaSourceOptionsSchema() (json.RawMessage, error) {
	return bindSchemaDocument(quotaSourceOptionsSchemaRef, json.RawMessage(`{
"type":"object",
"properties":{
  "scope":{"enum":["model","connection"]},
  "credentialHeader":{"type":"string","minLength":1,"pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
  "credentialPrefix":{"type":"string"},
  "windows":{"type":"array","maxItems":64,"items":{"type":"object","properties":{
    "name":{"type":"string","minLength":1},
    "itemsPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "aggregation":{"enum":["any_positive"]},
    "aggregationValuePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "aggregationStatusPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "aggregationStatusValues":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}},
    "namePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "usedPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "limitPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "remainingPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "remainingFromLimitUsed":{"type":"boolean"},
    "usedPercentPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "remainingPercentPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "resetAtPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "exhaustedStatusPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "exhaustedStatusValues":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}},
    "ignoreWhenPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
    "ignoreWhenValues":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}}
  },"anyOf":[{"required":["name"]},{"required":["namePath"]},{"required":["itemsPath"]}],
  "dependentRequired":{"aggregation":["aggregationValuePath","itemsPath","name"],"aggregationValuePath":["aggregation"],"aggregationStatusPath":["aggregationStatusValues"],"aggregationStatusValues":["aggregationStatusPath"],"remainingFromLimitUsed":["usedPath","limitPath"],"exhaustedStatusPath":["exhaustedStatusValues"],"exhaustedStatusValues":["exhaustedStatusPath"],"ignoreWhenPath":["ignoreWhenValues"],"ignoreWhenValues":["ignoreWhenPath"]},
  "not":{"anyOf":[{"required":["usedPercentPath","remainingPercentPath"]},{"required":["usedPercentPath","usedPath"]},{"required":["usedPercentPath","limitPath"]},{"required":["usedPercentPath","remainingPath"]},{"required":["remainingPercentPath","usedPath"]},{"required":["remainingPercentPath","limitPath"]},{"required":["remainingPercentPath","remainingPath"]}]},
  "additionalProperties":false}},
  "headerWindows":{"type":"array","maxItems":64,"items":{"type":"object","required":["name"],"properties":{
    "name":{"type":"string","minLength":1},
    "limitHeader":{"type":"string","pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
    "usedHeader":{"type":"string","pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
    "remainingHeader":{"type":"string","pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
    "resetHeader":{"type":"string","pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"}
  },"anyOf":[{"required":["limitHeader"]},{"required":["usedHeader"]},{"required":["remainingHeader"]},{"required":["resetHeader"]}],"additionalProperties":false}},
  "reports":{"type":"array","maxItems":8,"items":{"type":"object","required":["windows"],"properties":{
    "endpointOptions":{"type":"object","properties":{"path":{"type":"string"},"query":{"type":"object","additionalProperties":{"type":"string"}}},"anyOf":[{"required":["path"]},{"required":["query"]}],"additionalProperties":false},
    "windows":{"type":"array","minItems":1,"maxItems":64,"items":{"type":"object","properties":{
      "name":{"type":"string","minLength":1},
      "itemsPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "aggregation":{"enum":["any_positive"]},
      "aggregationValuePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "aggregationStatusPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "aggregationStatusValues":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}},
      "namePath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "usedPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "limitPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "remainingPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "remainingFromLimitUsed":{"type":"boolean"},
      "usedPercentPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "remainingPercentPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "resetAtPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "exhaustedStatusPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "exhaustedStatusValues":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}},
      "ignoreWhenPath":{"type":"string","pattern":"^/([^~]|~[01])*$"},
      "ignoreWhenValues":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}}
    },"anyOf":[{"required":["name"]},{"required":["namePath"]},{"required":["itemsPath"]}],
    "dependentRequired":{"aggregation":["aggregationValuePath","itemsPath","name"],"aggregationValuePath":["aggregation"],"aggregationStatusPath":["aggregationStatusValues"],"aggregationStatusValues":["aggregationStatusPath"],"remainingFromLimitUsed":["usedPath","limitPath"],"exhaustedStatusPath":["exhaustedStatusValues"],"exhaustedStatusValues":["exhaustedStatusPath"],"ignoreWhenPath":["ignoreWhenValues"],"ignoreWhenValues":["ignoreWhenPath"]},"not":{"anyOf":[{"required":["usedPercentPath","remainingPercentPath"]},{"required":["usedPercentPath","usedPath"]},{"required":["usedPercentPath","limitPath"]},{"required":["usedPercentPath","remainingPath"]},{"required":["remainingPercentPath","usedPath"]},{"required":["remainingPercentPath","limitPath"]},{"required":["remainingPercentPath","remainingPath"]}]},"additionalProperties":false}}
  },"additionalProperties":false}},
  "failureMode":{"enum":["fail_all","keep_available"]}
},
"dependentRequired":{"failureMode":["reports"]},
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
    "quotaHTTPStatuses":{"type":"array","uniqueItems":true,"items":{"type":"integer","minimum":400,"maximum":599}},
    "quotaMessageTokens":{"type":"array","items":{"type":"string","minLength":1}},
    "limitHeaders":{"type":"array","items":{"type":"object","required":["name"],"anyOf":[
      {"required":["limitHeader"]},{"required":["usedHeader"]},{"required":["remainingHeader"]},{"required":["exhaustedStatusHeader"]},{"required":["resetHeader"]}
    ],"dependentRequired":{"exhaustedStatusHeader":["exhaustedStatusValues"],"exhaustedStatusValues":["exhaustedStatusHeader"]},"properties":{
      "name":{"type":"string","minLength":1},
      "kind":{"type":"string"},
      "limitHeader":{"type":"string","minLength":1,"pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
      "usedHeader":{"type":"string","minLength":1,"pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
      "remainingHeader":{"type":"string","minLength":1,"pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
      "exhaustedStatusHeader":{"type":"string","minLength":1,"pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"},
      "exhaustedStatusValues":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}},
      "resetHeader":{"type":"string","minLength":1,"pattern":"^[!#$%&'*+.^_\\x60|~0-9A-Za-z-]+$"}
    },"additionalProperties":false}},
    "scopeSignals":{"type":"array","items":{"type":"object","required":["scope"],"properties":{
      "cause":{"enum":["request_invalid","capability_mismatch","model_not_found","auth","permission","quota_exhausted","rate_limited","capacity","overloaded","timeout","network","protocol","stream_failure","unknown"]},
      "codes":{"type":"array","items":{"type":"string","minLength":1}},
      "types":{"type":"array","items":{"type":"string","minLength":1}},
      "statuses":{"type":"array","items":{"type":"string","minLength":1}},
      "messageTokens":{"type":"array","items":{"type":"string","minLength":1}},
      "fields":{"type":"array","items":{"type":"object","required":["path"],"properties":{
        "path":{"type":"string","pattern":"^/([^~]|~[01])*$"},
        "equals":{"type":"array","items":{"type":"string","minLength":1}},
        "contains":{"type":"array","items":{"type":"string","minLength":1}}
      },"additionalProperties":false}},
      "scope":{"enum":["request","route","route_connection","connection","provider"]}
    },"additionalProperties":false}}
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
      "deviceAuthUrl":{"type":"string","format":"uri"},
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
