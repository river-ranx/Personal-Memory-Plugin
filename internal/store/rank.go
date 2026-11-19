package store

import (
	"math"
	"path/filepath"
	"strings"
)

// Signals is what ranking knows about one candidate besides its text match.
// Counts are over the last [statsWindowDays] days; DaysSinceUse is since the
// last time it was used or confirmed, or since it was written.
type Signals struct {
	// Relevance is how well the text matched, in [0, 1]: see [Relevance].
	Relevance    float64
	Project      ProjectRelation
	Uses         int
	Verified     int
	Contradicted int
	DaysSinceUse float64
}

// ProjectRelation is how a memory's project relates to the one asked from.
type ProjectRelation int

const (
	// ProjectUnknown: either side has no project.
	ProjectUnknown ProjectRelation = iota
	ProjectSame
	ProjectOther
)

// The factors. Decay runs from the last use, not from the write, so a rule
// set two years ago and used every week does not fade. (A superseded memory
// needs no factor: it is not returned.)
const (
	projectSameBoost   = 1.5
	projectOtherFactor = 0.9
	confirmWeight      = 0.4
	contradictWeight   = 0.6
	decayHalfLifeDays  = 90.0
	decayFloor         = 0.3
	statsWindowDays    = 30
)

// UnusedDecay is the decay at which a memory has gone unused for a quarter
// (the half-life): where a result says so.
const UnusedDecay = 0.5

// Decay is how much of its standing a memory keeps after days unused:
// halved every 90 days, never below 0.3. It is also what a client shows as
// "ranks at about N tenths of what it did".
func Decay(daysSinceUse float64) float64 {
	if daysSinceUse <= 0 {
		return 1
	}
	return math.Max(decayFloor, math.Pow(0.5, daysSinceUse/decayHalfLifeDays))
}

// Relevance turns FTS5's bm25() (negative, lower is better) into [0, 1]:
// half from the match on its own, r/(r+1), and half from how it compares
// with the best match of the same query.
//
// bm25 needs a corpus to mean anything: with a handful of memories a word's
// inverse document frequency collapses to almost zero and every match scores
// near nothing. The relative half keeps the best match of a small store
// worth something; once the store is large the absolute half tells a strong
// match from a weak one.
func Relevance(bm25, best float64) float64 {
	r, b := -bm25, -best
	if r <= 0 {
		return 0
	}
	relative := 1.0
	if b > r {
		relative = r / b
	}
	return 0.5*r/(r+1) + 0.5*relative
}

// Score is a candidate's rank: its relevance scaled by every signal.
func Score(s Signals) float64 {
	score := s.Relevance
	switch s.Project {
	case ProjectSame:
		score *= projectSameBoost
	case ProjectOther:
		score *= projectOtherFactor
	}
	score *= 1 + math.Log1p(float64(max(s.Uses, 0)))
	score *= 1 + confirmWeight*math.Log1p(float64(max(s.Verified, 0)))
	score /= 1 + contradictWeight*float64(max(s.Contradicted, 0))
	return score * Decay(s.DaysSinceUse)
}

// relateProjects compares a memory's project with the one asked from: see
// [SameProject].
func relateProjects(memory, asked *string) ProjectRelation {
	if memory == nil || asked == nil || NormalizeProject(*memory) == "" || NormalizeProject(*asked) == "" {
		return ProjectUnknown
	}
	if SameProject(*memory, *asked) {
		return ProjectSame
	}
	return ProjectOther
}

// SameProject is whether two projects are one: the same directory, or one
// inside the other, so a repository and its subdirectories are one project.
// Ranking, the header's note of another project and `list --project` all ask
// this, so they cannot disagree.
func SameProject(a, b string) bool {
	a, b = NormalizeProject(a), NormalizeProject(b)
	return a != "" && b != "" && (a == b || inside(a, b) || inside(b, a))
}

// inside is whether dir is below parent.
func inside(dir, parent string) bool {
	sep := string(filepath.Separator)
	return strings.HasPrefix(dir, strings.TrimSuffix(parent, sep)+sep)
}

// NormalizeProject is the one spelling of a project, which is the working
// directory an agent was started in: its absolute, cleaned path. `.` and the
// full path of the same directory are one project. Empty for none.
func NormalizeProject(project string) string {
	p := strings.TrimSpace(project)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}
