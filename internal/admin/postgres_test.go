package admin

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapWriteErrMapsPostgresConstraints(t *testing.T) {
	if err := mapWriteErr(&pgconn.PgError{Code: "23505"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unique_violation 应映射 ErrConflict，得到 %v", err)
	}
	if err := mapWriteErr(&pgconn.PgError{Code: "23503"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign_key_violation 应映射 ErrNotFound，得到 %v", err)
	}
}
