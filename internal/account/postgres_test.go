package account

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"linapi/internal/db"
)

// fakeQuerier 是 db.Querier 的测试替身，只实现本测试触达的方法。
type fakeQuerier struct {
	db.Querier
	getByUsernameFn  func(ctx context.Context, u string) (db.Account, error)
	getSettingFn     func(ctx context.Context, k string) (db.Setting, error)
	getByIDFn        func(ctx context.Context, id int64) (db.Account, error)
	updatePasswordFn func(ctx context.Context, arg db.UpdateAccountPasswordParams) error
}

func (f *fakeQuerier) GetAccountByUsername(ctx context.Context, u string) (db.Account, error) {
	return f.getByUsernameFn(ctx, u)
}
func (f *fakeQuerier) GetSetting(ctx context.Context, k string) (db.Setting, error) {
	return f.getSettingFn(ctx, k)
}
func (f *fakeQuerier) GetAccountByID(ctx context.Context, id int64) (db.Account, error) {
	return f.getByIDFn(ctx, id)
}
func (f *fakeQuerier) UpdateAccountPassword(ctx context.Context, arg db.UpdateAccountPasswordParams) error {
	return f.updatePasswordFn(ctx, arg)
}

func TestPGGetCredentials(t *testing.T) {
	ctx := context.Background()
	q := &fakeQuerier{
		getByUsernameFn: func(_ context.Context, u string) (db.Account, error) {
			if u == "ghost" {
				return db.Account{}, pgx.ErrNoRows
			}
			return db.Account{
				ID: 1, Username: u, PasswordHash: "bh", Role: RoleUser,
				ExternalID: pgtype.Text{String: u, Valid: true}, Enabled: true,
			}, nil
		},
	}
	s := &PGStore{q: q}

	cred, err := s.GetCredentials(ctx, "alice")
	if err != nil {
		t.Fatalf("GetCredentials 失败: %v", err)
	}
	if cred.PasswordHash != "bh" || cred.ExternalID != "alice" {
		t.Fatalf("凭证映射错误: %+v", cred)
	}
	if _, err := s.GetCredentials(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound, 得到 %v", err)
	}
}

func TestPGGetSettingsDefaults(t *testing.T) {
	ctx := context.Background()
	q := &fakeQuerier{
		getSettingFn: func(_ context.Context, _ string) (db.Setting, error) {
			return db.Setting{}, pgx.ErrNoRows // 键缺失 -> 回退默认。
		},
	}
	s := &PGStore{q: q}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get 设置失败: %v", err)
	}
	if got.RegistrationEnabled != DefaultRegistrationEnabled || got.NewUserInitialBalance != DefaultNewUserInitialBalance {
		t.Fatalf("缺失键应回退默认, 得到 %+v", got)
	}
}

func TestPGUpdatePasswordNotFound(t *testing.T) {
	ctx := context.Background()
	q := &fakeQuerier{
		getByIDFn: func(_ context.Context, _ int64) (db.Account, error) {
			return db.Account{}, pgx.ErrNoRows
		},
	}
	s := &PGStore{q: q}

	err := s.UpdatePassword(ctx, 999, "newhash")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("账户不存在应 ErrNotFound, 得到 %v", err)
	}
}

func TestPGUpdatePasswordOK(t *testing.T) {
	ctx := context.Background()
	q := &fakeQuerier{
		getByIDFn: func(_ context.Context, id int64) (db.Account, error) {
			return db.Account{ID: id, Username: "alice", Role: RoleUser, Enabled: true}, nil
		},
		updatePasswordFn: func(_ context.Context, arg db.UpdateAccountPasswordParams) error {
			return nil
		},
	}
	s := &PGStore{q: q}

	if err := s.UpdatePassword(ctx, 1, "newhash"); err != nil {
		t.Fatalf("账户存在时改密应成功, 得到 %v", err)
	}
}
