package ids

import "testing"

func TestNewIsPrefixedAndSorted(t *testing.T) {
	a, b := New(User), New(User)
	if !HasPrefix(a, User) || len(a) != len("usr_")+26 {
		t.Fatalf("bad id %q", a)
	}
	if a >= b {
		t.Fatalf("ids not monotonic: %s %s", a, b)
	}
}
