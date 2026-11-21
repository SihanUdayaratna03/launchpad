package main

import (
	"reflect"
	"strings"
	"testing"
)

const (
	shaA = "5f61837fd9ae8827e680d40686ec4f4135067911"
	shaB = "88505c6a1b2c3d4e5f60718293a4b5c6d7e8f901"
)

const host12 = "pr-12.13-211-45-67.nip.io"

func el(pr, sha string) interface{} {
	return map[string]interface{}{"prNumber": pr, "sha": sha, "host": "h" + pr}
}

func hostFor(pr string) string { return "h" + pr }

func TestUpsertAddsNewPR(t *testing.T) {
	got, res := upsertElement(nil, 12, shaA, hostFor("12"), "")
	if res != added {
		t.Fatalf("result = %v, want added", res)
	}
	if want := []interface{}{el("12", shaA)}; !reflect.DeepEqual(got, want) {
		t.Errorf("elements = %v, want %v", got, want)
	}
}

func TestUpsertSameCommitIsUnchanged(t *testing.T) {
	in := []interface{}{el("12", shaA)}
	got, res := upsertElement(in, 12, shaA, hostFor("12"), "")
	if res != unchanged {
		t.Fatalf("result = %v, want unchanged", res)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("elements changed: %v", got)
	}
}

func TestUpsertNewCommitUpdatesInPlace(t *testing.T) {
	in := []interface{}{el("7", shaA), el("12", shaA), el("20", shaA)}
	got, res := upsertElement(in, 12, shaB, hostFor("12"), "")
	if res != updated {
		t.Fatalf("result = %v, want updated", res)
	}
	want := []interface{}{el("7", shaA), el("12", shaB), el("20", shaA)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("elements = %v, want %v", got, want)
	}
}

func TestUpsertDoesNotMutateInput(t *testing.T) {
	in := []interface{}{el("12", shaA)}
	upsertElement(in, 12, shaB, hostFor("12"), "")
	upsertElement(in, 99, shaB, hostFor("99"), "")
	if want := []interface{}{el("12", shaA)}; !reflect.DeepEqual(in, want) {
		t.Errorf("input was mutated: %v", in)
	}
}

func TestUpsertKeepsOtherPRs(t *testing.T) {
	in := []interface{}{el("7", shaA)}
	got, _ := upsertElement(in, 12, shaB, hostFor("12"), "")
	if want := []interface{}{el("7", shaA), el("12", shaB)}; !reflect.DeepEqual(got, want) {
		t.Errorf("elements = %v, want %v", got, want)
	}
}

func TestUpsertIgnoresMalformedElements(t *testing.T) {
	in := []interface{}{"junk", 42, nil, el("12", shaA)}
	got, res := upsertElement(in, 12, shaB, hostFor("12"), "")
	if res != updated || len(got) != 4 {
		t.Fatalf("result = %v, len = %d", res, len(got))
	}
	if got[3].(map[string]interface{})["sha"] != shaB {
		t.Errorf("PR 12 was not updated: %v", got[3])
	}
}

func TestPrNumberMustMatchExactly(t *testing.T) {
	in := []interface{}{el("12", shaA)}
	got, res := upsertElement(in, 1, shaB, hostFor("1"), "")
	if res != added || len(got) != 2 {
		t.Errorf("PR 1 must not match PR 12: result = %v, len = %d", res, len(got))
	}
}

func TestRemoveElement(t *testing.T) {
	in := []interface{}{el("7", shaA), el("12", shaA), el("20", shaA)}

	got, removed := removeElement(in, 12)
	if !removed {
		t.Fatal("expected PR 12 to be removed")
	}
	if want := []interface{}{el("7", shaA), el("20", shaA)}; !reflect.DeepEqual(got, want) {
		t.Errorf("elements = %v, want %v", got, want)
	}
	if len(in) != 3 {
		t.Error("input was mutated")
	}

	if _, removed := removeElement(in, 99); removed {
		t.Error("removing an absent PR must report false")
	}
	if got, _ := removeElement(nil, 1); got == nil || len(got) != 0 {
		t.Errorf("removing from nil should give an empty list, got %v", got)
	}
}

func TestCommitPattern(t *testing.T) {
	tests := []struct {
		name string
		sha  string
		want bool
	}{
		{"valid", shaA, true},
		{"empty", "", false},
		{"short", shaA[:39], false},
		{"long", shaA + "0", false},
		{"uppercase", strings.ToUpper(shaA), false},
		{"non hex", strings.Repeat("g", 40), false},
		{"shell metacharacters", "$(id)" + shaA[5:], false},
		{"newline", shaA[:39] + "\n", false},
		{"image tag injection", "x:latest" + strings.Repeat("a", 32), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitPattern.MatchString(tt.sha); got != tt.want {
				t.Errorf("MatchString(%q) = %v, want %v", tt.sha, got, tt.want)
			}
		})
	}
}

func TestUpsertCarriesHost(t *testing.T) {
	got, _ := upsertElement(nil, 12, shaA, host12, "")
	m := got[0].(map[string]interface{})
	if m["host"] != host12 {
		t.Errorf("host = %v, want %v", m["host"], host12)
	}
}

func TestUpsertNewHostSameCommitUpdates(t *testing.T) {
	in := []interface{}{map[string]interface{}{"prNumber": "12", "sha": shaA, "host": "old"}}
	got, res := upsertElement(in, 12, shaA, host12, "")
	if res != updated {
		t.Fatalf("result = %v, want updated", res)
	}
	if got[0].(map[string]interface{})["host"] != host12 {
		t.Errorf("host not updated: %v", got[0])
	}
}

func TestUpsertLegacyElementWithoutHostUpdates(t *testing.T) {
	in := []interface{}{map[string]interface{}{"prNumber": "12", "sha": shaA}}
	_, res := upsertElement(in, 12, shaA, "", "")
	if res != updated {
		t.Errorf("result = %v, want updated so old elements gain a host key", res)
	}
}

func TestPreviewHost(t *testing.T) {
	tests := []struct {
		name, base string
		pr         int
		want       string
	}{
		{"normal", "13-211-45-67.nip.io", 12, "pr-12.13-211-45-67.nip.io"},
		{"no base domain", "", 12, ""},
		{"invalid base domain", "evil.com/x", 12, ""},
		{"uppercase rejected", "A.nip.io", 12, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewHost(tt.base, tt.pr); got != tt.want {
				t.Errorf("previewHost(%q, %d) = %q, want %q", tt.base, tt.pr, got, tt.want)
			}
		})
	}
}

func TestUpsertCarriesResolver(t *testing.T) {
	got, _ := upsertElement(nil, 12, shaA, host12, "le")
	m := got[0].(map[string]interface{})
	if m["tlsResolver"] != "le" {
		t.Errorf("tlsResolver = %v, want le", m["tlsResolver"])
	}
}

// An install without TLS must write exactly what it wrote before the key
// existed, or turning the feature off would rewrite every element.
func TestUpsertWithoutResolverLeavesTheKeyOut(t *testing.T) {
	got, _ := upsertElement(nil, 12, shaA, host12, "")
	if _, ok := got[0].(map[string]interface{})["tlsResolver"]; ok {
		t.Errorf("element has a tlsResolver key with no resolver set: %v", got[0])
	}
}

func TestUpsertSameResolverIsUnchanged(t *testing.T) {
	in := []interface{}{element(12, shaA, host12, "le")}
	got, res := upsertElement(in, 12, shaA, host12, "le")
	if res != unchanged {
		t.Fatalf("result = %v, want unchanged", res)
	}
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

// An element written before TLS was switched on gains the resolver, so the
// previews that already exist move to https without waiting for a new push.
func TestUpsertAddingResolverUpdates(t *testing.T) {
	in := []interface{}{element(12, shaA, host12, "")}
	got, res := upsertElement(in, 12, shaA, host12, "le")
	if res != updated {
		t.Fatalf("result = %v, want updated", res)
	}
	if got[0].(map[string]interface{})["tlsResolver"] != "le" {
		t.Errorf("resolver not added: %v", got[0])
	}
}

func TestUpsertDroppingResolverUpdates(t *testing.T) {
	in := []interface{}{element(12, shaA, host12, "le")}
	got, res := upsertElement(in, 12, shaA, host12, "")
	if res != updated {
		t.Fatalf("result = %v, want updated", res)
	}
	if _, ok := got[0].(map[string]interface{})["tlsResolver"]; ok {
		t.Errorf("resolver not removed: %v", got[0])
	}
}

func TestPreviewURL(t *testing.T) {
	tests := []struct {
		name, host, resolver, want string
	}{
		{"no resolver is plain http", "pr-12.1-2-3-4.sslip.io", "", "http://pr-12.1-2-3-4.sslip.io"},
		{"a resolver means https", "pr-12.1-2-3-4.sslip.io", "le", "https://pr-12.1-2-3-4.sslip.io"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewURL(tt.host, tt.resolver); got != tt.want {
				t.Errorf("previewURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolverPattern(t *testing.T) {
	tests := []struct {
		name, in string
		want     bool
	}{
		{"le", "le", true},
		{"with digits and dashes", "letsencrypt-staging2", true},
		{"empty", "", false},
		{"uppercase", "LE", false},
		{"leading digit", "1le", false},
		{"slash", "le/x", false},
		{"annotation injection", "le\ntraefik.ingress.kubernetes.io/router.tls: false", false},
		{"too long", strings.Repeat("a", 64), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolverPattern.MatchString(tt.in); got != tt.want {
				t.Errorf("resolverPattern(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
