package taskops

import (
	"fmt"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
)

func nativeLaunchArguments(
	harness string,
	arguments []string,
	generation string,
) ([]string, error) {
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
	return append(prefix, arguments...), nil
}

func validateNativeLaunch(harness string, arguments []string) error {
	if harness != "codex" && harness != "claude" {
		return fmt.Errorf("native launch generation is unsupported for harness %q", harness)
	}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			break
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
