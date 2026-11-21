package task

import "testing"

func TestShortIDsAvoidBothNeighboringIDsAndTaskNames(t *testing.T) {
	a := ID("01234567890000000000000000000000")
	b := ID("01234567900000000000000000000000")
	c := ID("fedcba98000000000000000000000000")
	got := ShortIDs([]Record{{Spec: Spec{ID: c, Name: "012345678"}}, {Spec: Spec{ID: b, Name: "other"}}, {Spec: Spec{ID: a, Name: "work"}}})
	if got[a] != "0123456789" || got[b] != "012345679" || got[c] != "fedcba98" {
		t.Fatal(got)
	}
}
