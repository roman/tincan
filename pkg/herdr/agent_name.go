package herdr

import (
	"fmt"
	"regexp"
)

var agentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidateAgentName checks the released Herdr command's agent-name contract.
func ValidateAgentName(name string) error {
	if !agentNamePattern.MatchString(name) {
		return fmt.Errorf("%w: agent name must match [a-z][a-z0-9_-]{0,31}", ErrResponse)
	}
	return nil
}
