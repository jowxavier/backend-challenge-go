package http

import (
	"github.com/jowxavier/backend-challenge-go/internal/observability"
	"net/http"
)

type responseStatus struct {
	http.ResponseWriter
	status int
}

func (w *responseStatus) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *responseStatus) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func loggedHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := &responseStatus{ResponseWriter: w}
		next.ServeHTTP(s, r)
		observability.Logger.Info("HTTP outcome", "method", r.Method, "route", r.Pattern, "status", s.status)
	})
}
