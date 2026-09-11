package store

import (
	"database/sql/driver"
	"fmt"
	"strconv"
)

// unsignedInt64 scans a BIGINT UNSIGNED column.
//
// It exists because database/sql has no unsigned integer in its value set: a
// driver hands back int64 where it fits and raw bytes where it does not, and
// scanning straight into an int64 fails on anything above 2^63.
//
// That is not a theoretical range. Replication seeds are derived by hashing,
// so roughly half of them land above the signed maximum, and without this a
// scenario with replications could be created and run but never listed.
type unsignedInt64 struct {
	N     uint64
	Valid bool
}

func (u *unsignedInt64) Scan(src any) error {
	u.Valid = false

	switch v := src.(type) {
	case nil:
		return nil

	case uint64:
		u.N, u.Valid = v, true
		return nil

	case int64:
		// A negative value here means a driver already wrapped the number
		// round; reinterpreting the bits recovers it.
		u.N, u.Valid = uint64(v), true
		return nil

	case []byte:
		return u.parse(string(v))

	case string:
		return u.parse(v)
	}

	return fmt.Errorf("cannot read %T as an unsigned integer", src)
}

func (u *unsignedInt64) parse(text string) error {
	if text == "" {
		return nil
	}

	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return fmt.Errorf("cannot read %q as an unsigned integer: %w", text, err)
	}

	u.N, u.Valid = n, true
	return nil
}

// Value writes the number back out.
//
// A large unsigned value is sent as text rather than as an int64, because the
// int64 conversion is exactly what loses it.
func (u unsignedInt64) Value() (driver.Value, error) {
	if !u.Valid {
		return nil, nil
	}
	if u.N > 1<<63-1 {
		return strconv.FormatUint(u.N, 10), nil
	}
	return int64(u.N), nil
}

// unsigned wraps a value for writing.
func unsigned(v uint64) unsignedInt64 {
	return unsignedInt64{N: v, Valid: true}
}

// unsignedOrNil wraps an optional value for writing.
func unsignedOrNil(p *uint64) any {
	if p == nil {
		return nil
	}
	return unsigned(*p)
}
