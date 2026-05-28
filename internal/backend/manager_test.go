package backend

import "testing"

func TestEnvListSortsAndFormatsValues(t *testing.T) {
	got := envList(map[string]string{
		"Z_TOKEN": "z",
		"A_TOKEN": "a",
	})
	want := []string{"A_TOKEN=a", "Z_TOKEN=z"}
	if len(got) != len(want) {
		t.Fatalf("env list length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env list[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIDsEqualSupportsStringIDs(t *testing.T) {
	if !idsEqual("42", "42") {
		t.Fatalf("expected string ID to match")
	}
	if idsEqual("43", "42") {
		t.Fatalf("expected different string ID not to match")
	}
}
