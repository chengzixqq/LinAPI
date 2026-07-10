package account

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"linapi/internal/db"
)

// PGStore 是 AccountStore + SettingsStore 的 PostgreSQL 实现。
// pool 供 CreateUserAccount 开事务；q 是绑定连接池的查询器，供非事务读写。
type PGStore struct {
	pool *pgxpool.Pool
	q    db.Querier
}

// NewPGStore 用连接池构造 PGStore。
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, q: db.New(pool)}
}

// mapErr 归一 pgx 写错误：无行 -> ErrNotFound，唯一冲突 -> ErrConflict。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}

// CreateUserAccount 在事务内建计费实体 + user 账户（external_id=username），原子提交。
func (s *PGStore) CreateUserAccount(ctx context.Context, username, passwordHash string, initialBalance int64) (Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 提交后 Rollback 为 no-op。

	qtx := db.New(tx)
	// 1. 建计费实体。
	if _, err := qtx.CreateUser(ctx, db.CreateUserParams{
		ExternalID: username, Balance: initialBalance, Enabled: true,
	}); err != nil {
		return Account{}, mapErr(err)
	}
	// 2. 建账户，external_id 关联计费实体。
	acc, err := qtx.CreateAccount(ctx, db.CreateAccountParams{
		Username: username, PasswordHash: passwordHash, Role: RoleUser,
		ExternalID: pgtype.Text{String: username, Valid: true},
	})
	if err != nil {
		return Account{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return accountFromDB(acc), nil
}

// CreateAccount 直接建管理员账户（bootstrap 用）；user 必须走 CreateUserAccount。
func (s *PGStore) CreateAccount(ctx context.Context, in CreateAccountInput) (Account, error) {
	if in.Role != RoleAdmin || in.ExternalID != "" {
		return Account{}, ErrInvalidRole
	}
	var ext pgtype.Text
	if in.ExternalID != "" {
		ext = pgtype.Text{String: in.ExternalID, Valid: true}
	}
	acc, err := s.q.CreateAccount(ctx, db.CreateAccountParams{
		Username: in.Username, PasswordHash: in.PasswordHash, Role: in.Role, ExternalID: ext,
	})
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) GetCredentials(ctx context.Context, username string) (Credentials, error) {
	acc, err := s.q.GetAccountByUsername(ctx, username)
	if err != nil {
		return Credentials{}, mapErr(err)
	}
	return Credentials{Account: accountFromDB(acc), PasswordHash: acc.PasswordHash}, nil
}

func (s *PGStore) GetByID(ctx context.Context, id int64) (Account, error) {
	acc, err := s.q.GetAccountByID(ctx, id)
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) GetByUsername(ctx context.Context, username string) (Account, error) {
	acc, err := s.q.GetAccountByUsername(ctx, username)
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) ListAccounts(ctx context.Context, limit, offset int) ([]Account, error) {
	rows, err := s.q.ListAccounts(ctx, db.ListAccountsParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows))
	for _, r := range rows {
		out = append(out, accountFromDB(r))
	}
	return out, nil
}

func (s *PGStore) CountAccounts(ctx context.Context) (int64, error) {
	return s.q.CountAccounts(ctx)
}

func (s *PGStore) SetEnabled(ctx context.Context, id int64, enabled bool) (Account, error) {
	acc, err := s.q.SetAccountEnabled(ctx, db.SetAccountEnabledParams{ID: id, Enabled: enabled})
	if err != nil {
		return Account{}, mapErr(err)
	}
	return accountFromDB(acc), nil
}

func (s *PGStore) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	// UpdateAccountPassword 是 :exec 语义，UPDATE 匹配 0 行也返回 nil，
	// 故先 GetAccountByID 确认账户存在（不存在时 mapErr 归一为 ErrNotFound），
	// 存在才执行更新。参考 internal/admin/postgres.go 的 AddBalance。
	if _, err := s.q.GetAccountByID(ctx, id); err != nil {
		return mapErr(err)
	}
	return mapErr(s.q.UpdateAccountPassword(ctx, db.UpdateAccountPasswordParams{ID: id, PasswordHash: passwordHash}))
}

func (s *PGStore) Get(ctx context.Context) (Settings, error) {
	row, err := s.q.GetSettingsSnapshot(ctx)
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		RegistrationEnabled:   parseBool(row.RegistrationEnabled, DefaultRegistrationEnabled),
		NewUserInitialBalance: parseInt64(row.NewUserInitialBalance, DefaultNewUserInitialBalance),
	}, nil
}

func (s *PGStore) Put(ctx context.Context, st Settings) error {
	return s.q.UpsertSettingsSnapshot(ctx, db.UpsertSettingsSnapshotParams{
		RegistrationEnabled:   formatBool(st.RegistrationEnabled),
		NewUserInitialBalance: formatInt64(st.NewUserInitialBalance),
	})
}

// accountFromDB 把 db.Account 转为领域视图（丢弃 password_hash）。
func accountFromDB(a db.Account) Account {
	return Account{
		ID:             a.ID,
		Username:       a.Username,
		Role:           a.Role,
		ExternalID:     a.ExternalID.String,
		GroupName:      a.GroupName,
		Enabled:        a.Enabled,
		SessionVersion: int(a.SessionVersion),
		CreatedAt:      a.CreatedAt.Time,
	}
}

// 编译期断言：PGStore 同时实现两个接口。
var (
	_ AccountStore  = (*PGStore)(nil)
	_ SettingsStore = (*PGStore)(nil)
)
