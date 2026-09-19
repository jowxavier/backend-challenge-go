package http

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/config"
	"go.uber.org/fx"
)

type Server struct {
	server *http.Server
}

func NewServer(lc fx.Lifecycle, cfg *config.Config, api *API) *Server {

	srv := &Server{
		server: &http.Server{
			Addr:              fmt.Sprintf("%s:%s", cfg.HTTP.Host, cfg.HTTP.Port),
			Handler:           api.Handler(),
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listener, err := net.Listen("tcp", srv.server.Addr)
			if err != nil {
				return err
			}
			go func() {
				if err := srv.server.Serve(listener); err != nil && err != http.ErrServerClosed {
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
