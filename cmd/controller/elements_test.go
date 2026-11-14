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

func el(pr, sha string) interface{} {
	return map[string]interface{}{"prNumber": pr, "sha": sha}
}

func TestUpsertAddsNewPR(t *testing.T) {
	got, res := upsertElement(nil, 12, shaA)
	if res != added {
		t.Fatalf("result = %v, want added", res)
	}
	if want := []interface{}{el("12", shaA)}; !reflect.DeepEqual(got, want) {
		t.Errorf("elements = %v, want %v", got, want)
	}
}

func TestUpsertSameCommitIsUnchanged(t *testing.T) {
	in := []interface{}{el("12", shaA)}
	got, res := upsertElement(in, 12, shaA)
	if res != unchanged {
		t.Fatalf("result = %v, want unchanged", res)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("elements changed: %v", got)
	}
}

func TestUpsertNewCommitUpdatesInPlace(t *testing.T) {
	in := []interface{}{el("7", shaA), el("12", shaA), el("20", shaA)}
	got, res := upsertElement(in, 12, shaB)
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
	upsertElement(in, 12, shaB)
	upsertElement(in, 99, shaB)
	if want := []interface{}{el("12", shaA)}; !reflect.DeepEqual(in, want) {
		t.Errorf("input was mutated: %v", in)
	}
}

func TestUpsertKeepsOtherPRs(t *testing.T) {
	in := []interface{}{el("7", shaA)}
	got, _ := upsertElement(in, 12, shaB)
	if want := []interface{}{el("7", shaA), el("12", shaB)}; !reflect.DeepEqual(got, want) {
		t.Errorf("elements = %v, want %v", got, want)
	}
}

func TestUpsertIgnoresMalformedElements(t *testing.T) {
	in := []interface{}{"junk", 42, nil, el("12", shaA)}
	got, res := upsertElement(in, 12, shaB)
	if res != updated || len(got) != 4 {
		t.Fatalf("result = %v, len = %d", res, len(got))
	}
	if got[3].(map[string]interface{})["sha"] != shaB {
		t.Errorf("PR 12 was not updated: %v", got[3])
	}
}

func TestPrNumberMustMatchExactly(t *testing.T) {
	in := []interface{}{el("12", shaA)}
	got, res := upsertElement(in, 1, shaB)
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
