package store

import "testing"

// Replication seeds are derived by hashing, so roughly half of them exceed the
// signed 64-bit maximum. This is the case that shipped broken: a scenario with
// replications could be created and run, but listing its runs failed with a
// scan error, because database/sql has no unsigned integer and the driver
// returns raw bytes for anything that does not fit an int64.
func TestUnsignedInt64SurvivesLargeValues(t *testing.T) {
	// A real seed from a replication, above 2^63.
	const large = uint64(10525136407462479985)

	cases := []struct {
		name string
		src  any
		want uint64
	}{
		{"raw bytes from the driver", []byte("10525136407462479985"), large},
		{"a string", "10525136407462479985", large},
		{"an int64 that fits", int64(777), 777},
		{"a uint64", large, large},
		{"the maximum", []byte("18446744073709551615"), ^uint64(0)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var u unsignedInt64
			if err := u.Scan(c.src); err != nil {
				t.Fatalf("Scan(%v): %v", c.src, err)
			}
			if !u.Valid {
				t.Fatal("the scanned value is not marked valid")
			}
			if u.N != c.want {
				t.Errorf("scanned %d, want %d", u.N, c.want)
			}
		})
	}
}

func TestUnsignedInt64HandlesNull(t *testing.T) {
	var u unsignedInt64
	if err := u.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if u.Valid {
		t.Error("a null column should not be marked valid")
	}

	v, err := u.Value()
	if err != nil || v != nil {
		t.Errorf("an invalid value should write as null, got %v (%v)", v, err)
	}
}

// A value above the signed maximum must go back out as text, because the int64
// conversion is exactly what loses it.
func TestUnsignedInt64RoundTrips(t *testing.T) {
	for _, n := range []uint64{0, 1, 777, 1 << 62, 1<<63 - 1, 1 << 63, ^uint64(0)} {
		written, err := unsigned(n).Value()
		if err != nil {
			t.Fatalf("Value() for %d: %v", n, err)
		}

		var back unsignedInt64
		if err := back.Scan(written); err != nil {
			t.Fatalf("Scan back %v: %v", written, err)
		}
		if back.N != n {
			t.Errorf("%d round-tripped to %d via %T", n, back.N, written)
		}
	}
}

func TestUnsignedInt64RejectsNonsense(t *testing.T) {
	var u unsignedInt64
	if err := u.Scan("not a number"); err == nil {
		t.Error("Scan accepted a value that is not a number")
	}
	if err := u.Scan(1.5); err == nil {
		t.Error("Scan accepted a float")
	}
}
