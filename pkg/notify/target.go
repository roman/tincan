package notify

import (
	"errors"
	"strings"
	"unicode"
)

var ErrInvalidTarget = errors.New("invalid agent target")

// Target identifies the agent that receives a doorbell.
type Target struct{ value string }

// ParseTarget preserves a nonblank adapter target without interpreting its grammar.
// Control characters are rejected because the target may cross a terminal boundary.
func ParseTarget(raw string) (Target, error) {
	if strings.TrimSpace(raw) == "" {
		return Target{}, ErrInvalidTarget
	}
	for _, character := range raw {
		if unicode.IsControl(character) {
			return Target{}, ErrInvalidTarget
		}
	}
	return Target{value: raw}, nil
}

func (target Target) String() string { return target.value }
func (target Target) valid() bool    { return target.value != "" }
