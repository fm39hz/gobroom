package kernel

import (
	"net/http"
)

// responseCommitWriter tracks the first externally visible response write.
// The caller may retry a semantic decode/render failure only before this
// boundary; a partially streamed response can never be silently restarted.
type responseCommitWriter struct {
	http.ResponseWriter
	committed bool
}

func (w *responseCommitWriter) WriteHeader(status int) {
	if w.committed {
		return
	}
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCommitWriter) Write(data []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
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
