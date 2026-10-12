package conciergecli

import (
	"fmt"
	"os"

	"github.com/roman/tincan/pkg/firstmate"
)

const workerLaunchGeneration = "CONCIERGE_SPAWN_GENERATION"

func (command *cli) validateWorkerLaunch(selected string) error {
	var generation string
	switch selected {
	case "task receive <task-id> <message-id>":
		generation = command.Task.Receive.Generation
	case "task ack <task-id> <message-id>":
		generation = command.Task.Ack.Generation
	case "task reply send <task-id>":
		generation = command.Task.Reply.Send.Generation
	default:
		return nil
	}
	launch := os.Getenv(workerLaunchGeneration)
	if err := firstmate.ValidateSpawnGeneration(launch); err != nil {
		return fmt.Errorf(
			"%s is missing or invalid in the worker launch context",
			workerLaunchGeneration,
		)
	}
	if launch != generation {
		return fmt.Errorf("--generation differs from the worker launch %s", workerLaunchGeneration)
	}
	return nil
}
