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
			server.executeNormalized(writer, request, normalize.Request{Model: "junior"})
			if committed {
				if writer.Code != http.StatusOK || writer.Body.String() != "already committed" {
					t.Fatalf("committed response rewritten: %d %s", writer.Code, writer.Body.String())
				}
				return
			}
			var body struct {
				Error struct{ Code, Message, Model string }
			}
			if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if writer.Code != http.StatusConflict || body.Error.Code != "continuity_restart_required" || body.Error.Model != "junior" || strings.Contains(body.Error.Message, "private") {
				t.Fatalf("unsafe restart response: %d %s", writer.Code, writer.Body.String())
			}
		})
	}
}
