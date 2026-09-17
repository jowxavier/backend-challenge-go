package http

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/config"
	"go.uber.org/fx"
)

type Server struct {
	server *http.Server
}

func NewServer(lc fx.Lifecycle, cfg *config.Config) *Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &Server{
		server: &http.Server{
			Addr:              fmt.Sprintf("%s:%s", cfg.HTTP.Host, cfg.HTTP.Port),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go func() {
				if err := srv.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					fmt.Printf("http server error: %v\n", err)
				}
			}()

			return nil
		},
		OnStop: func(ctx context.Context) error {
			return srv.server.Shutdown(ctx)
		},
	})

	return srv
}
