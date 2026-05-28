package backend

import "testing"

func TestIDsEqualSupportsStringIDs(t *testing.T) {
	if !idsEqual("42", "42") {
		t.Fatalf("expected string ID to match")
	}
	if idsEqual("43", "42") {
		t.Fatalf("expected different string ID not to match")
	}
}
