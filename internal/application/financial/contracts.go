package financial

import (
	"context"
	"errors"
	"github.com/jowxavier/backend-challenge-go/internal/application/events"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/domain/ledger"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"github.com/jowxavier/backend-challenge-go/internal/domain/wallet"
)

var (
	ErrInvalidInput         = errors.New("invalid financial input")
	ErrUnsupportedOperation = errors.New("unsupported financial operation")
	ErrInvalidContext       = errors.New("invalid wallet/player context")
	ErrConflict             = errors.New("financial idempotency conflict")
	ErrNotFound             = errors.New("record not found")
	ErrInvalidPersistedData = errors.New("invalid persisted financial data")
)

type ProcessRequest struct {
	ProviderID, IdempotencyKey, ExternalTransactionID string
	PlayerID, WalletID, RoundID, GameID               string
	Kind                                              wt.Kind
	Money                                             money.Money
	ReferenceExternalTransactionID                    string
}

type ProcessResult struct {
	TransactionID    string
	Status           wt.Status
	FailureCode      wt.FailureCode
	Balance          *money.Money
	IdempotentReplay bool
}

type Record struct {
	Transaction            *wt.WagerTransaction
	PayloadHash            [32]byte
	Balance                *money.Money
	ReferenceTransactionID string
	ReferenceDeadline      *time.Time
}

type Wallets interface {
	GetForUpdate(context.Context, string) (wallet.Wallet, error)
	UpdateBalance(context.Context, wallet.Wallet, int64) error
}

type Transactions interface {
	FindByProviderID(context.Context, string, string) (Record, error)
	GetForResolution(context.Context, string, string) (Record, error)
	HasSuccessfulReversal(context.Context, string, string) (bool, error)
	MarkPendingReference(context.Context, *wt.WagerTransaction, time.Time) error
	SetResolvedReference(context.Context, *wt.WagerTransaction, wt.Status, string) error
	FindFinancial(context.Context, string, string) (Record, error)
	TryInsertExternal(context.Context, *wt.WagerTransaction, [32]byte) (bool, error)
	CompleteOutcome(context.Context, *wt.WagerTransaction, wt.Status, money.Money) error
}

type Keys interface {
	Lookup(context.Context, string, string) (Record, error)
	Bind(context.Context, string, string, string) error
}

type Ledger interface {
	Insert(context.Context, ledger.Entry) error
}

type OutboxWriter interface {
	Insert(context.Context, events.Event) error
}

type Repositories struct {
	Inbox        Inbox
	Outbox       OutboxWriter
	Wallets      Wallets
	Transactions Transactions
	Keys         Keys
	Ledger       Ledger
}

type Transactor interface {
	WithinFinancialTransaction(context.Context, func(Repositories) error) error
}
