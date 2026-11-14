package main

import (
	"fmt"
	"regexp"
	"sync"
)

// previewMu serialises read-modify-write cycles on the ApplicationSet. Two
// webhooks arriving together would otherwise each read the same list and the
// second write would silently drop the first one's element.
var previewMu sync.Mutex

// A git commit SHA is 40 lowercase hex characters. The value ends up as a
// Helm parameter and an image tag, so anything else is rejected up front.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type change int

const (
	unchanged change = iota
	added
	updated
)

func element(pr int, sha string) map[string]interface{} {
	return map[string]interface{}{"prNumber": fmt.Sprintf("%d", pr), "sha": sha}
}

// upsertElement returns the list with PR pr at commit sha. It never mutates
// its input. A PR already at this commit is reported as unchanged, so a
// replayed webhook writes nothing.
func upsertElement(elements []interface{}, pr int, sha string) ([]interface{}, change) {
	want := fmt.Sprintf("%d", pr)

	for i, e := range elements {
		m, ok := e.(map[string]interface{})
		if !ok || m["prNumber"] != want {
			continue
		}
		if m["sha"] == sha {
			return elements, unchanged
		}
		next := append([]interface{}(nil), elements...)
		next[i] = element(pr, sha)
		return next, updated
	}

	next := append([]interface{}(nil), elements...)
	return append(next, element(pr, sha)), added
}

// removeElement returns the list without PR pr, and whether it was there.
func removeElement(elements []interface{}, pr int) ([]interface{}, bool) {
	want := fmt.Sprintf("%d", pr)
	kept := make([]interface{}, 0, len(elements))
	removed := false

	for _, e := range elements {
		if m, ok := e.(map[string]interface{}); ok && m["prNumber"] == want {
			removed = true
			continue
		}
		kept = append(kept, e)
	}

	return kept, removed
}
