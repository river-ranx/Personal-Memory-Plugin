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

// relateProjects compares a memory's project with the one asked from, after
// [NormalizeProject] on both: the same directory or not.
func relateProjects(memory, asked *string) ProjectRelation {
	if memory == nil || asked == nil {
		return ProjectUnknown
	}
	m, a := NormalizeProject(*memory), NormalizeProject(*asked)
	if m == "" || a == "" {
		return ProjectUnknown
	}
	if m == a {
		return ProjectSame
	}
	return ProjectOther
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
