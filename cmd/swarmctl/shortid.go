package main

// shortID returns at most n leading characters of id. Live cluster IDs are normally
// longer than that, but a listing must not panic on a short or absent one — a slice is
// not something a cluster's own ID field guarantees (#83).
func shortID(id string, n int) string {
	if len(id) > n {
		return id[:n]
	}
	return id
}
