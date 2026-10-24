package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/store"
)

type privateDecodeFailureIngress struct{ JSONOperationIngress }

func (privateDecodeFailureIngress) Decode(*http.Request) (normalize.Request, error) {
	return normalize.Request{}, errors.New("private-decoder-credential-4af1")
}

func TestIngressOrdinaryFailureDoesNotEchoPrivateError(t *testing.T) {
	state, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	server := NewServer(state)
	codec := privateDecodeFailureIngress{JSONOperationIngress{
		CodecID: "fixture-private-error", IngressContractVersion: 1,
		Operation: string(normalize.OperationChatGenerate), OperationContractVersion: 1,
		Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/private-failure"}},
	}}
	if err := server.RegisterOperationIngress(codec); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/private-failure", strings.NewReader(`{}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-decoder-credential-4af1") {
		t.Fatalf("ordinary decoder error crossed public boundary: %s", response.Body.String())
	}
}
