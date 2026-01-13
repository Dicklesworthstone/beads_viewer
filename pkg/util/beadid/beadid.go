package beadid

import (
	"strconv"
	"strings"
)

// CompareBeadID compares two bead/issue IDs using hierarchical segment rules.
// It splits IDs on '.' and compares segments pairwise. If both segments are
// numeric, they are compared numerically. Otherwise comparison is
// case-insensitive lexical. Returns -1 if a<b, 1 if a>b, 0 if equal.
func CompareBeadID(a, b string) int {
	sa := strings.Split(a, ".")
	sb := strings.Split(b, ".")
	na := len(sa)
	nb := len(sb)
	limit := na
	if nb < limit {
		limit = nb
	}
	for i := 0; i < limit; i++ {
		xa := sa[i]
		xb := sb[i]
		ia, errA := strconv.Atoi(xa)
		ib, errB := strconv.Atoi(xb)
		if errA == nil && errB == nil {
			if ia < ib {
				return -1
			}
			if ia > ib {
				return 1
			}
			// equal numeric -> continue
		} else {
			xaL := strings.ToLower(xa)
			xbL := strings.ToLower(xb)
			if xaL < xbL {
				return -1
			}
			if xaL > xbL {
				return 1
			}
			// equal lexical -> continue
		}
	}
	// All compared segments equal; shorter ID (parent) comes first
	if na < nb {
		return -1
	}
	if na > nb {
		return 1
	}
	return 0
}

// Less returns true if a < b according to CompareBeadID
func Less(a, b string) bool {
	return CompareBeadID(a, b) < 0
}
