package main

import (
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"github.com/jowxavier/backend-challenge-go/internal/infrastructure/oidcauth"
	"github.com/jowxavier/backend-challenge-go/internal/infrastructure/postgres"
	httpserver "github.com/jowxavier/backend-challenge-go/internal/interfaces/http"
	sqsmessaging "github.com/jowxavier/backend-challenge-go/internal/interfaces/messaging"
	"go.uber.org/fx"
)

func main() {
	fx.New(
		postgres.Module,
		sqsmessaging.Module,
		fx.Provide(
			config.Load,
			func(r *postgres.Runner, cfg *config.Config) (*financial.Processor, error) {
				return financial.NewProcessor(r, cfg.ReferencePendingTTL, time.Now)
			},
			postgres.NewWalletAPIStore,
			func(store *postgres.WalletAPIStore) *financial.WalletService {
				return financial.NewWalletService(store, time.Now)
			},
			oidcauth.NewLifecycle,
			httpserver.NewReadiness,
			func(p *financial.Processor, w *financial.WalletService, t *postgres.WagerTransactionRepository, v *oidcauth.Verifier, r *httpserver.Readiness) *httpserver.API {
				return httpserver.NewAPI(p, w, t, v, r)
			},
			httpserver.NewServer,
		),
		fx.Invoke(func(*httpserver.Server) {}),
	).Run()
}
