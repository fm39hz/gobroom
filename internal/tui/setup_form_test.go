package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
)

func setupFormFixture(t *testing.T, schema string) (daemon.AuthorizationSetupView, provider.AuthorizationAction, json.RawMessage) {
	t.Helper()
	ref := extensions.Ref{Kind: extensions.SchemaKind, ID: "test.auth.input", ContractVersion: 1}
	document, err := extensions.BindSchemaDocument(ref, json.RawMessage(schema))
	if err != nil {
		t.Fatal(err)
	}
	view, err := json.Marshal(extensions.CatalogView{Schemas: []extensions.Schema{{Ref: ref, Document: document}}})
	if err != nil {
		t.Fatal(err)
	}
	action := provider.AuthorizationAction{ID: "continue-safe", Kind: provider.AuthorizationInput, Label: "Continue", InputSchemaRef: &ref}
	return daemon.AuthorizationSetupView{SessionID: "session-safe", Status: "awaiting_user", Revision: 7, Actions: []provider.AuthorizationAction{action}}, action, view
}

func TestSetupFormTypedPayloadPreservesStringAndLargeInteger(t *testing.T) {
	view, action, catalog := setupFormFixture(t, `{"type":"object","properties":{"code":{"type":"string"},"count":{"type":"integer"}},"required":["code","count"],"additionalProperties":false}`)
	form, err := newSetupActionForm(view, action, catalog)
	if err != nil {
		t.Fatal(err)
	}
	form.inputs[0].SetValue("  code with spaces  ")
	form.inputs[1].SetValue("9007199254740993123456789")
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	if params["sessionID"] != view.SessionID || params["actionID"] != action.ID || params["revision"] != view.Revision {
		t.Fatalf("unexpected pinned context: %#v", params)
	}
	got := string(params["value"].(json.RawMessage))
	if !strings.Contains(got, `"code":"  code with spaces  "`) || !strings.Contains(got, `9007199254740993123456789`) {
		t.Fatalf("payload lost string whitespace or numeric precision: %s", got)
	}
}

func TestSetupFormMasksEveryInputAndValidationError(t *testing.T) {
	view, action, catalog := setupFormFixture(t, `{"type":"object","properties":{"secret":{"type":"string","minLength":3}},"required":["secret"]}`)
	form, err := newSetupActionForm(view, action, catalog)
	if err != nil {
		t.Fatal(err)
	}
	secret := "sensitive-value"
	form.inputs[0].SetValue(secret)
	if strings.Contains(form.inputs[0].View(), secret) {
		t.Fatal("setup input rendered its raw value")
	}
	form.inputs[0].SetValue("x")
	if _, err := form.Params(); err == nil || strings.Contains(err.Error(), `"x"`) {
		t.Fatalf("schema failure was missing or exposed input: %v", err)
	}
}

func TestSetupFormComplexSchemaUsesLosslessObjectFallback(t *testing.T) {
	view, action, catalog := setupFormFixture(t, `{"type":"object","properties":{"nested":{"type":"object","properties":{"n":{"type":"integer"}}}},"required":["nested"]}`)
	form, err := newSetupActionForm(view, action, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !form.setup.object || len(form.inputs) != 1 || form.fields[0].key != "setup:object" {
		t.Fatalf("complex schema did not use object fallback: %#v", form.fields)
	}
	form.inputs[0].SetValue(`{"nested":{"n":9007199254740993123456789}}`)
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(params["value"].(json.RawMessage)), "9007199254740993123456789") {
		t.Fatalf("fallback lost numeric precision: %s", params["value"])
	}
	form.inputs[0].SetValue(`{} {}`)
	if _, err := form.Params(); err == nil {
		t.Fatal("accepted more than one JSON value")
	}
}

func TestSetupFormResolvesLocalReferencedSchema(t *testing.T) {
	ref := extensions.Ref{Kind: extensions.SchemaKind, ID: "test.auth.nested", ContractVersion: 1}
	uri, err := ref.URI()
	if err != nil {
		t.Fatal(err)
	}
	view, action, rawCatalog := setupFormFixture(t, `{"type":"object","properties":{"nested":{"$ref":"`+uri+`"}},"required":["nested"]}`)
	var catalog extensions.CatalogView
	if err := json.Unmarshal(rawCatalog, &catalog); err != nil {
		t.Fatal(err)
	}
	catalog.Schemas = append(catalog.Schemas, extensions.Schema{Ref: ref, Document: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"` + uri + `","type":"object","properties":{"token":{"type":"string"}},"required":["token"]}`)})
	rawCatalog, err = json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	form, err := newSetupActionForm(view, action, rawCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if !form.setup.object {
		t.Fatal("reference-heavy input schema did not use JSON object fallback")
	}
	form.inputs[0].SetValue(`{"nested":{"token":"secret"}}`)
	if _, err := form.Params(); err != nil {
		t.Fatalf("local referenced schema was not validated: %v", err)
	}
	form.inputs[0].SetValue(`{"nested":{}}`)
	if _, err := form.Params(); err == nil {
		t.Fatal("local referenced schema constraints were not enforced")
	}
}

func TestSetupFormRejectsWrongStateAndSchemaReferences(t *testing.T) {
	view, action, catalog := setupFormFixture(t, `{"type":"object"}`)
	badView := view
	badView.Status = "completed"
	if _, err := newSetupActionForm(badView, action, catalog); err == nil {
		t.Fatal("accepted a setup view that is not awaiting user input")
	}
	badAction := action
	badRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "missing", ContractVersion: 1}
	badAction.InputSchemaRef = &badRef
	if _, err := newSetupActionForm(view, badAction, catalog); err == nil {
		t.Fatal("accepted a missing exact schema reference")
	}
	if _, err := newSetupActionForm(view, action, json.RawMessage(`{`)); err == nil {
		t.Fatal("accepted malformed catalog JSON")
	}
}

func TestSetupFormRejectsCatalogSchemaConflictAndOversize(t *testing.T) {
	view, action, catalogJSON := setupFormFixture(t, `{"type":"object"}`)
	var catalog extensions.CatalogView
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	conflict := catalog.Schemas[0]
	conflict.Document = json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://schemas.gobroom.invalid/schema/test.auth.input/1","type":"string"}`)
	catalog.Schemas = append(catalog.Schemas, conflict)
	encoded, _ := json.Marshal(catalog)
	if _, err := newSetupActionForm(view, action, encoded); err == nil {
		t.Fatal("accepted conflicting exact schema registrations")
	}

	view, action, catalogJSON = setupFormFixture(t, `{"type":"object"}`)
	form, err := newSetupActionForm(view, action, catalogJSON)
	if err != nil {
		t.Fatal(err)
	}
	form.inputs[0].SetValue(strings.Repeat("x", provider.MaxAuthorizationStateBytes+1))
	if _, err := form.Params(); err == nil {
		t.Fatal("accepted an oversized authorization payload")
	}
}

func TestWipeSetupInputClearsAndDetachesBinding(t *testing.T) {
	view, action, catalog := setupFormFixture(t, `{"type":"object"}`)
	form, err := newSetupActionForm(view, action, catalog)
	if err != nil {
		t.Fatal(err)
	}
	form.inputs[0].SetValue(`{"token":"secret"}`)
	form.wipeSetupInput()
	if form.inputs[0].Value() != "" || form.setup != nil {
		t.Fatal("setup form retained input or binding after wipe")
	}
	var nilForm *formState
	nilForm.wipeSetupInput()
}

func TestSetupFormConditionalCodeSchemaUsesFieldsAndOmitsUnusedBranch(t *testing.T) {
	view, action, catalog := setupFormFixture(t, `{"type":"object","properties":{"url":{"type":"string","minLength":1},"code":{"type":"string","minLength":1},"state":{"type":"string","minLength":1}},"additionalProperties":false,"oneOf":[{"required":["url"],"not":{"anyOf":[{"required":["code"]},{"required":["state"]}]}},{"required":["code","state"],"not":{"required":["url"]}}]}`)
	form, err := newSetupActionForm(view, action, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if form.setup.object || len(form.inputs) != 3 {
		t.Fatal("closed conditional shape lost its named fields")
	}
	for i, field := range form.setup.fields {
		if field.name == "url" {
			form.inputs[i].SetValue("https://return.test/callback?code=private")
		}
	}
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(params["value"].(json.RawMessage), &value) != nil || len(value) != 1 || value["url"] == nil {
		t.Fatal("unused code/state fields were not omitted")
	}
}

func TestSetupFormEnumOnlySchemaCyclesPublicChoices(t *testing.T) {
	view, action, catalog := setupFormFixture(t, `{"type":"object","properties":{"method":{"enum":["code","device"]}},"required":["method"],"additionalProperties":false}`)
	form, err := newSetupActionForm(view, action, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if form.setup.object {
		t.Fatal("enum-only method choice should not require a JSON object")
	}
	if form.cycleSetupOption(1) != "code" || form.cycleSetupOption(1) != "device" {
		t.Fatal("declared choices could not be selected")
	}
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	if string(params["value"].(json.RawMessage)) != `{"method":"device"}` {
		t.Fatal("enum selection did not bind its JSON type")
	}
}
