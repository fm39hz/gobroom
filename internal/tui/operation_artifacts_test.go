package tui

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
)

func TestDecodeProbeFileSpecs(t *testing.T) {
	got, err := decodeProbeFileSpecs(`[{"role":"source-audio","path":"/tmp/input.wav","mediaType":"audio/wav"}]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []daemon.ProbeFileSpec{{Role: "source-audio", Path: "/tmp/input.wav", MediaType: "audio/wav"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if got, err := decodeProbeFileSpecs(`[]`); err != nil || got == nil {
		t.Fatalf("empty array: %#v, %v", got, err)
	}
	if got, err := decodeProbeFileSpecs(" \t\n"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("blank optional array: %#v, %v", got, err)
	}
}

func TestDecodeProbeFileSpecsRejectsMalformedSpecs(t *testing.T) {
	for _, input := range []string{
		`[{"role":"x","path":"/tmp/x","other":"bad"}]`,
		`[{"role":" ","path":"/tmp/x"}]`,
		`[{"role":"x","path":""}]`,
		`[] {}`,
	} {
		if _, err := decodeProbeFileSpecs(input); err == nil {
			t.Errorf("decodeProbeFileSpecs(%q) succeeded", input)
		}
	}
}

func TestOperationArtifactFormParamsKeepFilesTyped(t *testing.T) {
	ref := extensions.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	definition := &provider.DefinitionMetadata{Operations: []provider.OperationMetadata{{ID: "audio.transcribe", TaskRef: &ref, Primitives: map[string]provider.PrimitiveRef{"endpoint": {ID: "e"}, "transport": {ID: "t"}, "requestCodec": {ID: "r"}, "responseDecoder": {ID: "d"}}}}}
	form := newOperationTestForm("conn-a", definition)
	if form == nil {
		t.Fatal("expected operation form")
	}
	form.inputs[0].SetValue("audio.transcribe")
	form.inputs[1].SetValue("vendor/asr")
	form.inputs[2].SetValue(`{"language":"en"}`)
	form.inputs[3].SetValue(`[{"role":"source-audio","path":"/tmp/a.wav","mediaType":"audio/wav"}]`)
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := params["artifactFiles"].([]daemon.ProbeFileSpec); !ok {
		t.Fatalf("artifactFiles type = %T", params["artifactFiles"])
	}
	if _, ok := params["payload"].(json.RawMessage); !ok {
		t.Fatalf("payload type = %T", params["payload"])
	}
	if params["operationID"] != "audio.transcribe" {
		t.Fatalf("operationID = %#v", params["operationID"])
	}
	request, files := operationProbeInput(params)
	if len(files) != 1 || files[0].Path != "/tmp/a.wav" {
		t.Fatalf("local files were not kept in the client input: %#v", files)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("/tmp/a.wav")) || bytes.Contains(encoded, []byte("artifactFiles")) {
		t.Fatalf("local file specification crossed the daemon request boundary: %s", encoded)
	}
}
