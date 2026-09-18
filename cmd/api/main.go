package main

import (
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"github.com/jowxavier/backend-challenge-go/internal/infrastructure/postgres"
	httpserver "github.com/jowxavier/backend-challenge-go/internal/interfaces/http"
	"go.uber.org/fx"
)

func main() {
	fx.New(
		postgres.Module,
		fx.Provide(
			config.Load,
			func(r *postgres.Runner, cfg *config.Config) (*financial.Processor, error) {
				return financial.NewProcessor(r, cfg.ReferencePendingTTL, time.Now)
			},
			httpserver.NewServer,
		),
		fx.Invoke(func(*httpserver.Server) {}),
	).Run()
}
