package store

import (
	"database/sql"
	"fmt"
)

// affectedOne turns an UPDATE or DELETE that matched nothing into ErrNotFound,
// so callers do not have to check RowsAffected at every call site.
func affectedOne(res sql.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, mapErr(err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}
