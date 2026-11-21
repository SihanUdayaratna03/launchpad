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

// A Traefik certificate resolver name. It ends up as an Ingress annotation
// value, so only what a resolver can actually be called is accepted.
var resolverPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// previewURL is the address of a preview served at host. A preview gets
// https only when a certificate resolver is configured to obtain its
// certificate.
func previewURL(host, resolver string) string {
	if resolver != "" {
		return "https://" + host
	}
	return "http://" + host
}

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

// element is one entry of the ApplicationSet's list. tlsResolver is left out
// when empty, so an install without TLS writes exactly what it always did.
func element(pr int, sha, host, resolver string) map[string]interface{} {
	e := map[string]interface{}{"prNumber": fmt.Sprintf("%d", pr), "sha": sha, "host": host}
	if resolver != "" {
		e["tlsResolver"] = resolver
	}
	return e
}

// upsertElement returns the list with PR pr at commit sha, served at host and
// certified by resolver (empty for none). It never mutates its input. A PR
// already at this commit, host and resolver is reported as unchanged, so a
// replayed webhook writes nothing.
func upsertElement(elements []interface{}, pr int, sha, host, resolver string) ([]interface{}, change) {
	want := fmt.Sprintf("%d", pr)

	for i, e := range elements {
		m, ok := e.(map[string]interface{})
		if !ok || m["prNumber"] != want {
			continue
		}
		have, _ := m["tlsResolver"].(string)
		if m["sha"] == sha && m["host"] == host && have == resolver {
			return elements, unchanged
		}
		next := append([]interface{}(nil), elements...)
		next[i] = element(pr, sha, host, resolver)
		return next, updated
	}

	next := append([]interface{}(nil), elements...)
	return append(next, element(pr, sha, host, resolver)), added
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
