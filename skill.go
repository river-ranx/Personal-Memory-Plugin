// Package memory carries the files the binary installs.
package memory

import _ "embed"

// Skill is the memory skill; {{MEMORY}} stands for the binary's path.
//
//go:embed skills/memory/SKILL.md
var Skill string
