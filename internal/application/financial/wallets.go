package financial

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/events"
	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

type WalletCreation struct {
	Wallet  wallet.Wallet
	Opening *wt.WagerTransaction
	Entry   ledger.Entry
	Events  []events.Event
}
type LedgerPage struct {
	Entries    []ledger.Entry
	NextCursor string
}
type Reconciliation struct {
	WalletID                       string
	Stored, Calculated, Difference money.Money
	Consistent                     bool
	CheckedEntries                 int64
}
type WalletStore interface {
	Create(context.Context, WalletCreation) error
	Get(context.Context, string) (wallet.Wallet, error)
	Ledger(context.Context, string, string, int) (LedgerPage, error)
	Reconcile(context.Context, string) (Reconciliation, error)
}
type WalletService struct {
	store       WalletStore
	now         func() time.Time
	divergences atomic.Uint64
	logger      *slog.Logger
}

func NewWalletService(store WalletStore, now func() time.Time) *WalletService {
	return &WalletService{store: store, now: now, logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
}
func (s *WalletService) Create(ctx context.Context, player string, initial money.Money) (wallet.Wallet, error) {
	id, err := newID()
	if err != nil {
		return wallet.Wallet{}, err
	}
	currency, err := initial.Currency()
	if err != nil {
		return wallet.Wallet{}, err
	}
	at := s.now().UTC().Truncate(time.Microsecond)
	w, err := wallet.New(id, player, currency, initial, at)
	if err != nil {
		return wallet.Wallet{}, err
	}
	n, _ := initial.MinorUnits()
	creation := WalletCreation{Wallet: w}
	if n > 0 {
		tid, err := newID()
		if err != nil {
			return wallet.Wallet{}, err
		}
		opening, err := wt.NewOpening(tid, player, id, initial, at)
		if err != nil {
			return wallet.Wallet{}, err
		}
		lid, err := newID()
		if err != nil {
			return wallet.Wallet{}, err
		}
		zero, _ := money.Zero(currency)
		entry, err := ledger.New(lid, id, tid, ledger.Credit, initial, zero, initial, at)
		if err != nil {
			return wallet.Wallet{}, err
		}
		processed, err := events.NewOpeningProcessed(opening)
		if err != nil {
			return wallet.Wallet{}, err
		}
		changed, err := events.NewBalanceChanged(entry, 1)
		if err != nil {
			return wallet.Wallet{}, err
		}
		creation.Opening = opening
		creation.Entry = entry
		creation.Events = []events.Event{processed, changed}
	}
	if err = s.store.Create(ctx, creation); err != nil {
		return wallet.Wallet{}, err
	}
	return w, nil
}
func (s *WalletService) Get(ctx context.Context, id string) (wallet.Wallet, error) {
	return s.store.Get(ctx, id)
}
func (s *WalletService) Ledger(ctx context.Context, id, cursor string, limit int) (LedgerPage, error) {
	if limit < 1 || limit > 100 {
		return LedgerPage{}, ErrInvalidInput
	}
	return s.store.Ledger(ctx, id, cursor, limit)
}
func (s *WalletService) Reconcile(ctx context.Context, id string) (Reconciliation, error) {
	r, err := s.store.Reconcile(ctx, id)
	if err == nil && !r.Consistent {
		s.divergences.Add(1)
		s.logger.Warn("wallet reconciliation mismatch", "walletId", id)
	}
	return r, err
}
func (s *WalletService) Divergences() uint64 { return s.divergences.Load() }
