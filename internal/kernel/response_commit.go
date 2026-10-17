package kernel

import (
	"context"
	"errors"
	"net/http"
)

var ErrResponseOutputLimit = errors.New("operation response exceeded its output byte limit")

// responseCommitWriter tracks the first externally visible response write.
// This does not prove upstream replay safety: an accepted effect remains
// accepted even before the first client byte. A committed stream never retries.
type responseCommitWriter struct {
	http.ResponseWriter
	committed bool
	maxBytes  int64
	written   int64
	ctx       context.Context
	failure   error
}

func (w *responseCommitWriter) Committed() bool { return w != nil && w.committed }

func (w *responseCommitWriter) WriteHeader(status int) {
	if w.failure != nil {
		return
	}
	if w.ctx != nil && w.ctx.Err() != nil {
		return
	}
	if w.committed {
		return
	}
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCommitWriter) Write(data []byte) (int, error) {
	if w.failure != nil {
		return 0, w.failure
	}
	if w.ctx != nil && w.ctx.Err() != nil {
		return 0, w.latchFailure(WrapResponseFailure(ResponseFailureClient, w.ctx.Err()))
	}
	if w.maxBytes > 0 && int64(len(data)) > w.maxBytes-w.written {
		return 0, w.latchFailure(WrapResponseFailure(ResponseFailureRenderer, ErrResponseOutputLimit))
	}
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.written += int64(n)
	return n, w.latchFailure(WrapResponseFailure(ResponseFailureClient, err))
}

func (w *responseCommitWriter) Flush() {
	_ = w.FlushError()
}

func (w *responseCommitWriter) latchFailure(err error) error {
	if err != nil && w.failure == nil {
		w.failure = err
	}
	return err
}

func (w *responseCommitWriter) FlushError() error {
	if w.failure != nil {
		return w.failure
	}
	if w.ctx != nil && w.ctx.Err() != nil {
		return w.latchFailure(WrapResponseFailure(ResponseFailureClient, w.ctx.Err()))
	}
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.latchFailure(WrapResponseFailure(ResponseFailureClient, http.NewResponseController(w.ResponseWriter).Flush()))
}

func (w *responseCommitWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
