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

var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// previewHost is where PR pr is served. An unset or malformed base domain
// gives no host, which makes the chart skip the Ingress.
func previewHost(base string, pr int) string {
	if !domainPattern.MatchString(base) {
		return ""
	}
	return fmt.Sprintf("pr-%d.%s", pr, base)
}

type change int

const (
	unchanged change = iota
	added
	updated
)

func element(pr int, sha, host string) map[string]interface{} {
	return map[string]interface{}{"prNumber": fmt.Sprintf("%d", pr), "sha": sha, "host": host}
}

// upsertElement returns the list with PR pr at commit sha, served at host. It
// never mutates its input. A PR already at this commit and host is reported as
// unchanged, so a replayed webhook writes nothing.
func upsertElement(elements []interface{}, pr int, sha, host string) ([]interface{}, change) {
	want := fmt.Sprintf("%d", pr)

	for i, e := range elements {
		m, ok := e.(map[string]interface{})
		if !ok || m["prNumber"] != want {
			continue
		}
		if m["sha"] == sha && m["host"] == host {
			return elements, unchanged
		}
		next := append([]interface{}(nil), elements...)
		next[i] = element(pr, sha, host)
		return next, updated
	}

	next := append([]interface{}(nil), elements...)
	return append(next, element(pr, sha, host)), added
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
