package randx

import "testing"

func TestUint64nBounds(t *testing.T) {
	tests := []struct {
		name string
		n    uint64
	}{
		{name: "small", n: 2},
		{name: "power of two", n: 64},
		{name: "non-power of two", n: 100},
		{name: "large", n: 1 << 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(12345)
			for i := 0; i < 10000; i++ {
				v := p.Uint64n(tt.n)
				if v >= tt.n {
					t.Fatalf("Uint64n(%d) = %d, out of range", tt.n, v)
				}
			}
		})
	}
}

func TestUint64nPanicsOnZero(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for n == 0")
		}
	}()
	New(1).Uint64n(0)
}

func TestFloat64Range(t *testing.T) {
	p := New(99)
	for i := 0; i < 10000; i++ {
		v := p.Float64()
		if v < 0 || v >= 1 {
			t.Fatalf("Float64() = %v, out of [0,1)", v)
		}
	}
}

func TestDeterminism(t *testing.T) {
	tests := []struct {
		name string
		seed uint64
	}{
		{name: "zero seed remapped", seed: 0},
		{name: "small seed", seed: 1},
		{name: "large seed", seed: 0xDEADBEEFCAFEF00D},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(tt.seed)
			b := New(tt.seed)
			for i := 0; i < 1000; i++ {
				if a.Uint64() != b.Uint64() {
					t.Fatal("same seed produced different sequence")
				}
			}
		})
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	a := New(1)
	b := New(2)
	identical := true
	for i := 0; i < 100; i++ {
		if a.Uint64() != b.Uint64() {
			identical = false
			break
		}
	}
	if identical {
		t.Fatal("different seeds produced identical sequences")
	}
}

func TestStateRoundTrip(t *testing.T) {
	p := New(7)
	for i := 0; i < 50; i++ {
		p.Uint64()
	}
	state := p.State()

	// A fresh generator restored to the same state must continue identically.
	q := New(7)
	for i := 0; i < 50; i++ {
		q.Uint64()
	}
	q.Restore(state)

	for i := 0; i < 100; i++ {
		if p.Uint64() != q.Uint64() {
			t.Fatal("restored generator diverged")
		}
	}
}

func TestCopyIndependent(t *testing.T) {
	a := New(5)
	b := a.Copy()
	a.Uint64()
	if a.State() == b.State() {
		t.Fatal("copy shares state with original")
	}
	if a.Uint64() == b.Uint64() {
		t.Fatal("advancing original changed copy")
	}
}
