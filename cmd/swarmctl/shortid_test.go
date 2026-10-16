package main

import "testing"

// TestShortID pins the contract the listings rely on: an absent or short ID is returned
// as it is, and a longer one is cut to n. A listing must not panic on either (#83).
func TestShortID(t *testing.T) {
	tests := []struct {
		id   string
		n    int
		want string
	}{
		{id: "", n: 12, want: ""},
		{id: "abc", n: 12, want: "abc"},
		{id: "0123456789ab", n: 12, want: "0123456789ab"},
		{id: "0123456789abc", n: 12, want: "0123456789ab"},
	}
	for _, tt := range tests {
		if got := shortID(tt.id, tt.n); got != tt.want {
			t.Errorf("shortID(%q, %d) = %q, want %q", tt.id, tt.n, got, tt.want)
		}
	}
}
