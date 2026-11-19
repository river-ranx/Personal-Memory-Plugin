package store

import (
	"errors"
	"fmt"
)

// The write gate keeps out text too short to say anything — a word or two
// ("报销", "release", "好") — which will never help a later session. The
// store is on this machine and only its owner reads it, so there is no privacy
// rule here: a token or a phone number is stored as written.
//
// Whether a sentence is worth keeping — a lesson, or only a record of what
// happened — is not judged here: no pattern can tell "初稿已发给导师" from
// "初稿要先发给导师", and a gate that guesses refuses what should be kept.
// That judgment is the agent's, made from the prompts and the skill. A repeat
// of a memory already kept is the duplicate check's.

// ErrRefused is a draft the gate will not store.
var ErrRefused = errors.New("refused")

// minContentTokens is how many words (see [tokens]) a memory's text must have:
// three is a short sentence, and a word or two is a label, which says nothing
// to whoever finds it. The text has to say it itself: a title and keywords are
// ways to find a memory, not what it says, so they do not count.
const minContentTokens = 3

// screen is the gate: nil lets the draft in, an [ErrRefused] keeps it out
// and says how to write it instead. It holds for every kind.
func screen(d Draft) error {
	if len(tokens(d.Content)) < minContentTokens {
		return fmt.Errorf("%w: too short to be a memory (under %d searchable words). Write what, where and why, in a sentence",
			ErrRefused, minContentTokens)
	}
	return nil
}
