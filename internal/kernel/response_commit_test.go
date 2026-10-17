package kernel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseCommitWriterEnforcesOperationOutputBound(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseCommitWriter{ResponseWriter: recorder, maxBytes: 4}
	if n, err := writer.Write([]byte("12345")); n != 0 || !errors.Is(err, ErrResponseOutputLimit) || writer.committed {
		t.Fatalf("oversized first write n=%d err=%v committed=%v", n, err, writer.committed)
	}
	if n, err := writer.Write([]byte("1")); n != 0 || !errors.Is(err, ErrResponseOutputLimit) || recorder.Body.Len() != 0 {
		t.Fatal("latched output-bound failure allowed later bytes")
	}
	writer = &responseCommitWriter{ResponseWriter: recorder, maxBytes: 4}
	if n, err := writer.Write([]byte("1234")); n != 4 || err != nil || !writer.committed {
		t.Fatalf("bounded write n=%d err=%v committed=%v", n, err, writer.committed)
	}
	if n, err := writer.Write([]byte("5")); n != 0 || !errors.Is(err, ErrResponseOutputLimit) || recorder.Body.String() != "1234" {
		t.Fatalf("overflow write n=%d err=%v body=%q", n, err, recorder.Body.String())
	}
}

type flushFailureWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w flushFailureWriter) FlushError() error { return w.err }

func TestResponseCommitWriterRetainsClientFlushFailure(t *testing.T) {
	cause := errors.New("client flush failed")
	base := flushFailureWriter{httptest.NewRecorder(), cause}
	writer := &responseCommitWriter{ResponseWriter: base}
	if err := http.NewResponseController(writer).Flush(); !errors.Is(err, cause) || responseFailureOrigin(err) != ResponseFailureClient {
		t.Fatalf("flush provenance lost: %v", err)
	}
	if _, err := writer.Write([]byte("must not follow failed flush")); !errors.Is(err, cause) || base.Body.Len() != 0 {
		t.Fatal("failed client flush did not stop later writes")
	}
}

func TestResponseCommitWriterStopsWritesAndFlushAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := httptest.NewRecorder()
	writer := &responseCommitWriter{ResponseWriter: base, ctx: ctx}
	writer.WriteHeader(http.StatusAccepted)
	if _, err := writer.Write([]byte("not visible")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if err := http.NewResponseController(writer).Flush(); !errors.Is(err, context.Canceled) || writer.committed || base.Body.Len() != 0 {
		t.Fatal("cancelled writer committed output")
	}
}
