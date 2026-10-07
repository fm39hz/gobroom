package kernel

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestResponseCommitWriterEnforcesOperationOutputBound(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseCommitWriter{ResponseWriter: recorder, maxBytes: 4}
	if n, err := writer.Write([]byte("12345")); n != 0 || !errors.Is(err, ErrResponseOutputLimit) || writer.committed {
		t.Fatalf("oversized first write n=%d err=%v committed=%v", n, err, writer.committed)
	}
	if n, err := writer.Write([]byte("1234")); n != 4 || err != nil || !writer.committed {
		t.Fatalf("bounded write n=%d err=%v committed=%v", n, err, writer.committed)
	}
	if n, err := writer.Write([]byte("5")); n != 0 || !errors.Is(err, ErrResponseOutputLimit) || recorder.Body.String() != "1234" {
		t.Fatalf("overflow write n=%d err=%v body=%q", n, err, recorder.Body.String())
	}
}
