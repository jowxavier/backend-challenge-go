package main

import (
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
			httpserver.NewServer,
		),
		fx.Invoke(func(*httpserver.Server) {}),
	).Run()
}
