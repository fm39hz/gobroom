package externalprovider

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/provider"
)

const jobClientEnvelopeSchemaID = "example.jobs.client-envelope"

var jobClientEnvelopeSchemaRef = provider.Ref{Kind: provider.SchemaKind, ID: jobClientEnvelopeSchemaID, ContractVersion: 1}

type jobEventObserver struct {
	mu     sync.Mutex
	events map[jobObservationKey][]provider.ResponseEvent
}

type jobObservationKey struct {
	action      string
	correlation string
}

const jobCorrelationHeader = "X-Fixture-Job-Correlation"

func newJobEventObserver() *jobEventObserver {
	return &jobEventObserver{events: make(map[jobObservationKey][]provider.ResponseEvent)}
}

func (o *jobEventObserver) record(key jobObservationKey, event provider.ResponseEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	event.Raw = append([]byte(nil), event.Raw...)
	event.Opaque = append(json.RawMessage(nil), event.Opaque...)
	event.Artifacts = append([]provider.ArtifactRef(nil), event.Artifacts...)
	for index := range event.Artifacts {
		event.Artifacts[index] = event.Artifacts[index].Clone()
	}
	o.events[key] = append(o.events[key], event)
}

func (o *jobEventObserver) take(action string) []provider.ResponseEvent {
	return o.takeCorrelated(jobObservationKey{action: action})
}

func (o *jobEventObserver) takeCorrelated(key jobObservationKey) []provider.ResponseEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	events := o.events[key]
	delete(o.events, key)
	return events
}

const jobObservedDecoderPrefix = "fixture.job-result-observer."

func jobObservedDecoderID(action string) string { return jobObservedDecoderPrefix + action }

type jobObservedDecoder struct {
	jobResponseDecoder
	observer *jobEventObserver
}

func (d jobObservedDecoder) ID() string { return jobObservedDecoderID(d.action) }

func (d jobObservedDecoder) Decode(ctx context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, hooks provider.StreamHooks) error {
	key := jobObservationKey{action: d.action, correlation: response.Headers.Get(jobCorrelationHeader)}
	return d.jobResponseDecoder.Decode(ctx, response, func(event provider.ResponseEvent) error {
		d.observer.record(key, event)
		return emit(event)
	}, hooks)
}

type jobObservedModule struct{ observer *jobEventObserver }

func (jobObservedModule) ModuleDescriptor() provider.ModuleRef {
	return (JobModule{}).ModuleDescriptor()
}

func (m jobObservedModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := (JobModule{}).Register(runtime); err != nil {
		return err
	}
	for _, action := range []string{"create", "status", "cancel"} {
		if err := runtime.RegisterResponseDecoderVersion(jobObservedDecoder{jobResponseDecoder: jobResponseDecoder{action: action}, observer: m.observer}, 2); err != nil {
			return err
		}
	}
	return nil
}

func observedJobManifest(t *testing.T) []byte {
	t.Helper()
	definition := jobManifest()
	for _, action := range []string{"create", "status", "cancel"} {
		operation := provider.Operation(jobOperation(action).ID)
		binding, exists := definition.Operations[operation]
		if !exists {
			t.Fatalf("job manifest is missing operation %q", operation)
		}
		binding.ResponseDecoder.ID = jobObservedDecoderID(action)
		binding.ResponseDecoder.ContractVersion = 2
		definition.Operations[operation] = binding
	}
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func jobClientEnvelopeSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"result":{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":128},"status":{"enum":["pending","running","completed","cancelled"]}},"required":["id","status"],"additionalProperties":false},"artifacts":{"type":"array","maxItems":1,"items":{"type":"object","properties":{"typeRef":{"type":"object","properties":{"kind":{"const":"artifact"},"id":{"const":"example.jobs.handle"},"contractVersion":{"const":1}},"required":["kind","id","contractVersion"],"additionalProperties":false},"role":{"const":"job"},"owner":{"type":"object","properties":{"domain":{"const":"provider"},"issuerRef":{"type":"object","properties":{"kind":{"const":"provider-definition"},"id":{"const":"example-jobs"},"contractVersion":{"const":1}},"required":["kind","id","contractVersion"],"additionalProperties":false},"providerDefinitionId":{"const":"example-jobs"},"connectionId":{"type":"string","minLength":1},"modelIdentity":{"type":"string"},"clientContract":{"type":"string"},"sessionId":{"type":"string"}},"required":["domain","issuerRef","providerDefinitionId","connectionId"],"additionalProperties":false},"replayScope":{"const":"connection"},"sensitivity":{"const":"private"},"mediaType":{"const":"application/json"},"sizeBytes":{"type":"integer","minimum":1,"maximum":512},"expiresAt":{"type":"string","format":"date-time"},"value":{"type":"string"}},"required":["typeRef","role","owner","replayScope","sensitivity","mediaType","sizeBytes","expiresAt","value"],"additionalProperties":false}}},"required":["result"],"additionalProperties":false}`)
}

func newJobClientEnvelopeValidator(t *testing.T, observer *jobEventObserver) func(json.RawMessage) error {
	t.Helper()
	runtime, err := provider.NewRuntimeRegistryWithModules(jobObservedModule{observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterSchemaDocument(jobClientEnvelopeSchemaRef, jobClientEnvelopeSchema()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.LoadDefinitionJSON(observedJobManifest(t)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return func(value json.RawMessage) error {
		return snapshot.ValidateSchema(jobClientEnvelopeSchemaRef, value)
	}
}

func assertJobResultEvents(t *testing.T, events []provider.ResponseEvent, expected jobResult, wantHandle bool) {
	t.Helper()
	textEvents, resultArtifacts, handleArtifacts := 0, 0, 0
	now := time.Now()
	var got jobResult
	for _, event := range events {
		if event.Kind == provider.EventTextDelta {
			textEvents++
		}
		if event.Kind != provider.EventArtifact {
			continue
		}
		for _, artifact := range event.Artifacts {
			switch {
			case artifact.TypeRef == jobResultArtifact && artifact.Role == "result":
				resultArtifacts++
				if artifact.ReplayScope != provider.ArtifactReplayRequest || artifact.MediaType != "application/json" {
					t.Fatalf("result artifact replay/media contract changed: %#v", artifact)
				}
				if err := decodeJobResult(artifact.Value, &got); err != nil {
					t.Fatalf("decode observed result artifact: %v", err)
				}
			case artifact.TypeRef == jobArtifact && artifact.Role == "job":
				handleArtifacts++
				if artifact.ReplayScope != provider.ArtifactReplayConnection || artifact.ExpiresAt.Before(now.Add(4*time.Minute)) || artifact.ExpiresAt.After(now.Add(5*time.Minute+5*time.Second)) {
					t.Fatalf("job handle replay/expiry contract changed: %#v", artifact)
				}
			}
		}
	}
	wantHandleArtifacts := 0
	if wantHandle {
		wantHandleArtifacts = 1
	}
	if textEvents != 0 || resultArtifacts != 1 || got != expected || handleArtifacts != wantHandleArtifacts {
		t.Fatalf("result did not traverse typed artifact path: text=%d resultArtifacts=%d result=%#v handleArtifacts=%d events=%#v", textEvents, resultArtifacts, got, handleArtifacts, events)
	}
}

func assertCorrelatedJobCreateEvents(t *testing.T, events []provider.ResponseEvent, expected jobResult, expectedAccount string) {
	t.Helper()
	assertJobResultEvents(t, events, expected, true)
	completeEvents := 0
	for _, event := range events {
		if event.Kind == provider.EventResponseComplete {
			completeEvents++
		}
		if event.Kind != provider.EventArtifact {
			continue
		}
		for _, artifact := range event.Artifacts {
			if artifact.Owner.ConnectionID != expectedAccount {
				t.Fatalf("correlated artifact crossed caller accounts: got %q want %q", artifact.Owner.ConnectionID, expectedAccount)
			}
			if artifact.TypeRef == jobArtifact && artifact.Role == "job" {
				var handle jobHandle
				if err := json.Unmarshal(artifact.Value, &handle); err != nil || handle.ID != expected.ID {
					t.Fatalf("correlated handle id=%q want upstream id=%q: %v", handle.ID, expected.ID, err)
				}
			}
		}
	}
	if completeEvents != 1 {
		t.Fatalf("correlated caller received %d completion events, want 1", completeEvents)
	}
}

func TestJobResultDecodersDeclareArtifactEventsWithoutText(t *testing.T) {
	runtime, err := provider.NewRuntimeRegistryWithModules(JobModule{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(jobManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.LoadDefinitionJSON(manifest); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"create", "status", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ref := provider.Ref{
				Kind:            string(provider.PrimitiveResponseDecoder),
				ID:              "example.jobs." + action,
				ContractVersion: 2,
			}
			_, implementation, err := snapshot.Bind(ref, nil)
			if err != nil {
				t.Fatal(err)
			}
			decoder, ok := implementation.(provider.ResponseDecoder)
			if !ok {
				t.Fatalf("registered implementation %q is not a response decoder", ref.ID)
			}

			declared := make(map[provider.ResponseEventKind]bool)
			for _, event := range decoder.PossibleEvents() {
				declared[event] = true
			}
			if declared[provider.EventTextDelta] || !declared[provider.EventArtifact] {
				t.Fatalf("%s result event contract must declare artifact and omit text delta; got %v", action, decoder.PossibleEvents())
			}
		})
	}
}

func TestJobResultProjectorBuildsVersionedArtifactReference(t *testing.T) {
	projector := &jobResultProjector{max: 4096}
	metadata := json.RawMessage(`{"kind":"artifact","content":{"artifacts":[{"typeRef":{"kind":"artifact","id":"example.jobs.result","contractVersion":1},"role":"result","mediaType":"application/json","sizeBytes":37,"sensitivity":"private"}]}}`)
	if err := projector.ConsumeEvent(metadata); err != nil {
		t.Fatal(err)
	}
	got, err := projector.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":2,"typeRef":{"kind":"artifact","id":"example.jobs.result","contractVersion":1},"role":"result","mediaType":"application/json","sizeBytes":37}`
	if string(got) != want {
		t.Fatalf("logical job result=%s, want %s", got, want)
	}
}

func TestJobResultProjectorRejectsAbsentDuplicateWrongTypeAndOversizedReferences(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{name: "absent", event: `{"kind":"response_complete","content":{}}`},
		{name: "wrong type", event: `{"kind":"artifact","content":{"artifacts":[{"typeRef":{"kind":"artifact","id":"example.jobs.handle","contractVersion":1},"role":"result","mediaType":"application/json","sizeBytes":37}]}}`},
		{name: "duplicate", event: `{"kind":"artifact","content":{"artifacts":[{"typeRef":{"kind":"artifact","id":"example.jobs.result","contractVersion":1},"role":"result","mediaType":"application/json","sizeBytes":37},{"typeRef":{"kind":"artifact","id":"example.jobs.result","contractVersion":1},"role":"result","mediaType":"application/json","sizeBytes":37}]}}`},
		{name: "oversized", event: `{"kind":"artifact","content":{"artifacts":[{"typeRef":{"kind":"artifact","id":"example.jobs.result","contractVersion":1},"role":"result","mediaType":"application/json","sizeBytes":4097}]}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projector := &jobResultProjector{max: 4096}
			err := projector.ConsumeEvent(json.RawMessage(test.event))
			if test.name == "duplicate" || test.name == "oversized" {
				if err == nil {
					t.Fatal("invalid artifact metadata was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := projector.Finalize(); err == nil {
				t.Fatal("missing result reference was finalized")
			}
		})
	}
}

func TestJobRendererRejectsMissingDuplicateAndWrongTypeResults(t *testing.T) {
	validValue := []byte(`{"id":"job-renderer","status":"pending"}`)
	validArtifact := provider.ArtifactRef{
		TypeRef: jobResultArtifact, Role: "result", MediaType: "application/json", SizeBytes: int64(len(validValue)),
		Value: validValue,
	}
	tests := []struct {
		name          string
		artifacts     []provider.ArtifactRef
		wantEmitError bool
	}{
		{name: "missing"},
		{name: "duplicate", artifacts: []provider.ArtifactRef{validArtifact, validArtifact}, wantEmitError: true},
		{name: "wrong type", artifacts: []provider.ArtifactRef{{TypeRef: jobArtifact, Role: "result", MediaType: "application/json", SizeBytes: 34, Value: validArtifact.Value}}, wantEmitError: true},
		{name: "malformed value", artifacts: []provider.ArtifactRef{{TypeRef: jobResultArtifact, Role: "result", MediaType: "application/json", SizeBytes: 1, Value: []byte("{")}}, wantEmitError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			session, err := (jobRenderer{}).Begin(context.Background(), provider.ResponseRenderContext{}, writer)
			if err != nil {
				t.Fatal(err)
			}
			err = session.Emit(context.Background(), provider.ResponseEvent{Kind: provider.EventArtifact, Artifacts: test.artifacts})
			if (err != nil) != test.wantEmitError {
				t.Fatalf("Emit error=%v, wantEmitError=%v", err, test.wantEmitError)
			}
			if test.wantEmitError {
				return
			}
			if err := session.Finish(context.Background(), nil); err == nil {
				t.Fatal("renderer finalized without exactly one typed result")
			}
		})
	}
}

func TestJobClientEnvelopeSchemaIsRegisteredSeparately(t *testing.T) {
	observer := newJobEventObserver()
	validate := newJobClientEnvelopeValidator(t, observer)
	valid := json.RawMessage(`{"result":{"id":"job-schema","status":"completed"}}`)
	if err := validate(valid); err != nil {
		t.Fatalf("client envelope was not validated by its own schema: %v", err)
	}
	withResultArtifact := json.RawMessage(`{"result":{"id":"job-schema","status":"completed"},"artifacts":[{"typeRef":{"kind":"artifact","id":"example.jobs.result","contractVersion":1},"role":"result"}]}`)
	if err := validate(withResultArtifact); err == nil {
		t.Fatal("client envelope schema accepted the result replay artifact")
	}
}
