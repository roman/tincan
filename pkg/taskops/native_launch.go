package taskops

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

func nativeResumeArguments(harness string, reference *herdr.SessionReference) ([]string, error) {
	if harness != "codex" && harness != "claude" {
		return nil, fmt.Errorf("unsupported resume harness %q", harness)
	}
	if reference == nil || reference.Source != "herdr:"+harness ||
		reference.Agent != harness || reference.Kind != "id" {
		return nil, fmt.Errorf(
			"native %s session ID is unavailable from its Herdr integration",
			harness,
		)
	}
	id := reference.Value
	if strings.TrimSpace(id) == "" || len(id) > 512 || !utf8.ValidString(id) ||
		strings.IndexFunc(
			id,
			unicode.IsControl,
		) >= 0 || strings.HasPrefix(strings.TrimSpace(id), "-") {
		return nil, fmt.Errorf("native %s session ID cannot be used as a resume argument", harness)
	}
	if harness == "claude" {
		return []string{"--resume", id}, nil
	}
	return []string{"resume", id}, nil
}

func nativeLaunchArguments(
	harness string,
	arguments []string,
	generation, protocol string,
) ([]string, error) {
	if strings.TrimSpace(protocol) == "" || strings.IndexFunc(protocol, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("worker protocol must be a nonempty shell-safe native argument")
	}
	if err := firstmate.ValidateSpawnGeneration(generation); err != nil {
		return nil, fmt.Errorf("invalid launch generation: %w", err)
	}
	if err := validateNativeLaunch(harness, arguments); err != nil {
		return nil, err
	}
	var prefix []string
	switch harness {
	case "codex":
		prefix = []string{
			"-c",
			fmt.Sprintf("shell_environment_policy.set.CONCIERGE_SPAWN_GENERATION=%q", generation),
		}
	case "claude":
		prefix = []string{
			"--settings",
			fmt.Sprintf(`{"env":{"CONCIERGE_SPAWN_GENERATION":%q}}`, generation),
		}
	}
	return append(append(prefix, arguments...), protocol), nil
}

func validateNativeLaunch(harness string, arguments []string) error {
	if harness != "codex" && harness != "claude" {
		return fmt.Errorf("native launch generation is unsupported for harness %q", harness)
	}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			return fmt.Errorf(
				"Concierge owns the native protocol prompt; caller arguments cannot contain --",
			)
		}
		if harness == "claude" {
			if argument == "--settings" || strings.HasPrefix(argument, "--settings=") {
				return fmt.Errorf(
					"reusable Claude arguments cannot contain --settings; use user or project settings and supply generation separately at launch",
				)
			}
			continue
		}
		var assignment string
		switch {
		case argument == "-c" || argument == "--config":
			index++
			if index == len(arguments) {
				return fmt.Errorf("Codex %s requires a configuration assignment", argument)
			}
			assignment = arguments[index]
		case strings.HasPrefix(argument, "--config="):
			assignment = strings.TrimPrefix(argument, "--config=")
		case strings.HasPrefix(argument, "-c"):
			assignment = strings.TrimPrefix(strings.TrimPrefix(argument, "-c"), "=")
		default:
			continue
		}
		key, _, _ := strings.Cut(assignment, "=")
		key = strings.NewReplacer(`"`, "", "'", "", " ", "", "\t", "").Replace(key)
		if key == "shell_environment_policy" ||
			strings.HasPrefix(key, "shell_environment_policy.") {
			return fmt.Errorf(
				"reusable Codex arguments cannot override shell_environment_policy; supply generation separately at launch",
			)
		}
	}
	return nil
}
