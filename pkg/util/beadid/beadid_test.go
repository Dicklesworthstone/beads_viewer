package beadid

import "testing"

func TestCompareBeadID_BasicHierarchy(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"bd-a3f8", "bd-a3f8.1", -1},
		{"bd-a3f8.1", "bd-a3f8.1.1", -1},
		{"bd-a3f8.1.1", "bd-a3f8.1", 1},
	}
	for _, c := range cases {
		if got := CompareBeadID(c.a, c.b); got != c.want {
			t.Fatalf("CompareBeadID(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareBeadID_NumericSegments(t *testing.T) {
	// verify Compare directly
	if CompareBeadID("1.2", "1.10") != -1 {
		t.Fatalf("expected 1.2 < 1.10")
	}
	if CompareBeadID("1.10", "1.2") != 1 {
		t.Fatalf("expected 1.10 > 1.2")
	}
	// equal
	if CompareBeadID("bd-x", "bd-x") != 0 {
		t.Fatalf("expected equal")
	}
}

func TestCompareBeadID_MixedAlphaNumeric(t *testing.T) {
	in := []struct {
		a, b string
		want int
	}{
		{"bd-a3f8", "bd-b234", -1},
		{"bd-a3f8.2", "bd-a3f8.10", -1},
		{"Bd-X.2", "bd-x.10", -1},
	}
	for _, c := range in {
		if got := CompareBeadID(c.a, c.b); got != c.want {
			t.Fatalf("CompareBeadID(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}
