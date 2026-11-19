package store

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDecayHalvesEveryNinetyDaysAndStopsAtThreeTenths(t *testing.T) {
	for _, c := range []struct{ days, want float64 }{
		{-5, 1}, {0, 1}, {90, 0.5}, {180, 0.3}, {1000, 0.3},
	} {
		if got := Decay(c.days); !near(got, c.want) {
			t.Fatalf("Decay(%v) = %v, want %v", c.days, got, c.want)
		}
	}
	if got := Decay(45); !(got > 0.5 && got < 1) {
		t.Fatalf("Decay(45) = %v, want between 0.5 and 1", got)
	}
}

func TestEachSignalMovesTheScoreTheWayLbMemDoes(t *testing.T) {
	base := Signals{Relevance: 0.75}
	b := Score(base)
	if !near(b, 0.75) {
		t.Fatalf("relevance alone = %v", b)
	}
	with := func(change func(*Signals)) float64 {
		s := base
		change(&s)
		return Score(s)
	}
	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"same project", with(func(s *Signals) { s.Project = ProjectSame }), b * 1.5},
		{"other project", with(func(s *Signals) { s.Project = ProjectOther }), b * 0.9},
		{"used", with(func(s *Signals) { s.Uses = 3 }), b * (1 + math.Log(4))},
		{"verified", with(func(s *Signals) { s.Verified = 2 }), b * (1 + 0.4*math.Log(3))},
		{"contradicted", with(func(s *Signals) { s.Contradicted = 2 }), b / 2.2},
		{"unused for 90 days", with(func(s *Signals) { s.DaysSinceUse = 90 }), b * 0.5},
	}
	for _, c := range checks {
		if !near(c.got, c.want) {
			t.Fatalf("%s: %v, want %v", c.name, c.got, c.want)
		}
	}
	if Score(Signals{}) != 0 {
		t.Fatal("no text match scores nothing")
	}
}

// The best match of a small store still counts; a weaker one counts less
// than the best in proportion; a strong match in a large store keeps most of
// its absolute half.
func TestRelevanceHoldsUpWhenTheStoreIsTiny(t *testing.T) {
	if got := Relevance(-1e-6, -1e-6); got < 0.5 {
		t.Fatalf("the best of a tiny store = %v", got)
	}
	if got := Relevance(-0.5e-6, -1e-6); !(got > 0.25 && got < 0.26) {
		t.Fatalf("half as good as the best = %v", got)
	}
	if got := Relevance(-9, -9); !near(got, 0.5*0.9+0.5) {
		t.Fatalf("a strong best match = %v", got)
	}
	if Relevance(0, -1) != 0 {
		t.Fatal("no match is no relevance")
	}
}

// A project is a working directory: its absolute path, however it was spelled.
func TestAProjectIsTheWorkingDirectoryAsAnAbsolutePath(t *testing.T) {
	cwd, _ := os.Getwd()
	for in, want := range map[string]string{
		"/work/alpha":      "/work/alpha",
		" /work/alpha/ ":   "/work/alpha",
		"/work/./alpha":    "/work/alpha",
		"/work/x/../alpha": "/work/alpha",
		".":                cwd,
		"sub":              filepath.Join(cwd, "sub"),
		"":                 "",
		"   ":              "",
	} {
		if got := NormalizeProject(in); got != want {
			t.Fatalf("NormalizeProject(%q) = %q, want %q", in, got, want)
		}
	}
	a, b, c := "/work/alpha/", "/work/alpha", "/work/beta"
	if relateProjects(&a, &b) != ProjectSame {
		t.Fatal("two spellings of one directory are the same project")
	}
	if relateProjects(&a, &c) != ProjectOther {
		t.Fatal("two directories are two projects")
	}
	// A repository and its subdirectories are one project, either way round;
	// a directory that only starts with the same letters is not.
	sub, lookalike := "/work/alpha/docs/ch3", "/work/alpha-2"
	if relateProjects(&sub, &b) != ProjectSame || relateProjects(&b, &sub) != ProjectSame {
		t.Fatal("a subdirectory is the same project")
	}
	if relateProjects(&lookalike, &b) != ProjectOther {
		t.Fatal("a look-alike directory is another project")
	}
	if relateProjects(&a, nil) != ProjectUnknown {
		t.Fatal("no project asked is unknown, not other")
	}
}
