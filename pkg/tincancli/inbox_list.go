package tincancli

import (
	"fmt"
	"io"
	"strings"

	"github.com/roman/tincan/pkg/inbox"
)

func messageType(key string) string {
	prefix, _, namespaced := strings.Cut(key, ":")
	if !namespaced || prefix == "" {
		return "message"
	}
	characters := []rune(prefix)
	if len(characters) > 32 {
		return string(characters[:32]) + "…"
	}
	return prefix
}

func writeCompactList(
	output io.Writer,
	root string,
	recipient inbox.Recipient,
	messages []inbox.Message,
) {
	fmt.Fprintf(output, "total: %d\nreturned: %d\ntruncated: false\n", len(messages), len(messages))
	if len(messages) == 0 {
		fmt.Fprintln(output, "messages[0]:")
	} else {
		fmt.Fprintf(output, "messages[%d]{id,type}:\n", len(messages))
		for _, message := range messages {
			fmt.Fprintf(output, "  %s,%s\n", message.ID, quote(messageType(message.Key)))
		}
	}
	// The hint must keep the selected root, including shell metacharacters.
	command := "tincan --root '" + strings.ReplaceAll(root, "'", "'\\''") + "' inbox"
	fmt.Fprintf(output, "help[2]:\n  %s\n  %s\n",
		quote(command+" read "+recipient.String()+" MESSAGE_ID"),
		quote(command+" list "+recipient.String()+" --full"))
}
