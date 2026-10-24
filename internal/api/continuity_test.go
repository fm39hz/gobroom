package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestContinuityRestartErrorIsSafeAndDoesNotOverwriteCommittedResponse(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			server := Server{executor: func(_ context.Context, _ normalize.Request, writer http.ResponseWriter) error {
				if committed {
					_, _ = writer.Write([]byte("already committed"))
				}
				return fmt.Errorf("private provider reference: %w", kernel.ErrContinuityRestartRequired)
			}}
			writer := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			server.executeNormalized(writer, request, normalize.Request{Model: "junior"}, continuityFailureIngress())
			if committed {
				if writer.Code != http.StatusOK || writer.Body.String() != "already committed" {
					t.Fatalf("committed response rewritten: %d %s", writer.Code, writer.Body.String())
				}
				return
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Type    string `json:"type"`
					Message string `json:"message"`
					Model   string `json:"model"`
				} `json:"error"`
			}
			if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if writer.Code != http.StatusConflict || body.Error.Code != string(kernel.ClientFailureContinuityRestart) || body.Error.Type != "invalid_request_error" || body.Error.Model != "junior" || body.Error.Message != kernel.ErrContinuityRestartRequired.Error() || strings.Contains(body.Error.Message, "private") {
				t.Fatalf("unsafe restart response: %d %s", writer.Code, writer.Body.String())
			}
		})
	}
}

func TestTypedClientFailureIsProjectedSafely(t *testing.T) {
	detail := kernel.ClientFailure{Code: kernel.ClientFailureCode("safe_custom_code"), Type: "invalid_request_error", Message: "safe public message"}
	server := Server{executor: func(context.Context, normalize.Request, http.ResponseWriter) error {
		return kernel.NewClientFailureError(detail, fmt.Errorf("private upstream detail"))
	}}
	writer := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	server.executeNormalized(writer, request, normalize.Request{Model: "junior"}, continuityFailureIngress())
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
			Model   string `json:"model"`
		} `json:"error"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if writer.Code != http.StatusBadGateway || body.Error.Code != string(detail.Code) || body.Error.Type != detail.Type || body.Error.Message != detail.Message || body.Error.Model != "junior" || strings.Contains(writer.Body.String(), "private upstream detail") {
		t.Fatalf("typed failure response was not projected safely: %d %s", writer.Code, writer.Body.String())
	}
}

func continuityFailureIngress() JSONOperationIngress {
	return JSONOperationIngress{CodecID: "fixture-continuity", IngressContractVersion: 1, Operation: string(normalize.OperationChatGenerate), OperationContractVersion: 1}
}
