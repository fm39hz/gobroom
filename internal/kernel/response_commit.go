package kernel

import (
	"errors"
	"net/http"
)

var ErrResponseOutputLimit = errors.New("operation response exceeded its output byte limit")

// responseCommitWriter tracks the first externally visible response write.
// The caller may retry a semantic decode/render failure only before this
// boundary; a partially streamed response can never be silently restarted.
type responseCommitWriter struct {
	http.ResponseWriter
	committed bool
	maxBytes  int64
	written   int64
}

func (w *responseCommitWriter) Committed() bool { return w != nil && w.committed }

func (w *responseCommitWriter) WriteHeader(status int) {
	if w.committed {
		return
	}
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCommitWriter) Write(data []byte) (int, error) {
	if w.maxBytes > 0 && int64(len(data)) > w.maxBytes-w.written {
		return 0, ErrResponseOutputLimit
	}
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.written += int64(n)
	return n, err
}

func (w *responseCommitWriter) Flush() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseCommitWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
