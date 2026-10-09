package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// RingOutcome reports whether the doorbell was submitted or withheld.
type RingOutcome uint8

const (
	RingNotAttempted RingOutcome = iota
	RingSent
	RingSkipped
	RingFailed
	RingUnavailable
)

// RingResult separates an unsafe or unavailable endpoint from a command failure.
type RingResult struct {
	Outcome RingOutcome
	Err     error
}

// Ring submits an existing doorbell only when the visible composer holds that exact line.
func (client Client) Ring(ctx context.Context, target Target, line string) RingResult {
	if target.session == "" || target.paneID == "" || line == "" ||
		strings.ContainsFunc(line, unicode.IsControl) {
		return RingResult{RingFailed, ErrResponse}
	}
	if result, stop := client.ringHealth(ctx, target); stop {
		return result
	}
	kind, err := client.ringKind(ctx, target)
	if err != nil {
		return RingResult{RingFailed, err}
	}
	if ringGlyph(kind) == "" {
		return RingResult{Outcome: RingSkipped}
	}
	// Health is sampled before the final viewport read for each mutation.
	if result, stop := client.ringHealth(ctx, target); stop {
		return result
	}
	view, err := client.ringRead(ctx, target)
	if err != nil {
		return RingResult{RingFailed, err}
	}
	content, proved := bareComposer(kind, view)
	if !proved || content != line {
		return RingResult{Outcome: RingSkipped}
	}
	if err := client.ringCommand(ctx, target, "pane", "send-keys", target.paneID, "enter"); err != nil {
		return RingResult{RingFailed, err}
	}
	// A successful Enter may already have delivered the line. A later health
	// change cannot establish that delivery failed, even if the endpoint disappears.
	if err := ringWait(ctx); err != nil {
		return RingResult{RingFailed, err}
	}
	status := client.Health(ctx, target)
	if status.Endpoint != Alive || status.Activity != HealthIdle {
		return RingResult{Outcome: RingSent}
	}
	view, err = client.ringRead(ctx, target)
	if err != nil {
		return RingResult{RingFailed, err}
	}
	content, proved = bareComposer(kind, view)
	if proved && content == line {
		if err := client.ringCommand(ctx, target, "pane", "send-keys", target.paneID, "enter"); err != nil {
			return RingResult{RingFailed, err}
		}
	}
	return RingResult{Outcome: RingSent}
}

func (client Client) ringHealth(ctx context.Context, target Target) (RingResult, bool) {
	status := client.Health(ctx, target)
	switch status.Endpoint {
	case Dead, Missing:
		return RingResult{Outcome: RingUnavailable}, true
	case Alive:
		if status.Activity == HealthIdle {
			return RingResult{}, false
		}
	}
	return RingResult{Outcome: RingSkipped}, true
}

func (client Client) ringKind(ctx context.Context, target Target) (string, error) {
	stdout, stderr, err := client.sessionRead(ctx, target, "agent", "get", target.paneID)
	if err != nil {
		return "", fmt.Errorf("%w: agent get: %v", ErrCommand, err)
	}
	result, code := healthBody(stdout, stderr)
	if code != "" {
		return "", fmt.Errorf("%w: agent get: %s", ErrCommand, code)
	}
	var response struct {
		Agent struct {
			PaneID string `json:"pane_id"`
			Kind   string `json:"agent"`
		} `json:"agent"`
	}
	if json.Unmarshal(result, &response) != nil || response.Agent.PaneID != target.paneID ||
		response.Agent.Kind == "" {
		return "", ErrResponse
	}
	return response.Agent.Kind, nil
}

func (client Client) ringRead(ctx context.Context, target Target) (string, error) {
	stdout, stderr, err := client.sessionRead(
		ctx,
		target,
		"pane",
		"read",
		target.paneID,
		"--source",
		"visible",
		"--format",
		"ansi",
	)
	if err != nil {
		return "", fmt.Errorf("%w: pane read: %v", ErrCommand, err)
	}
	_, code := healthBody(stdout, stderr)
	if code != "" {
		return "", fmt.Errorf("%w: pane read: %s", ErrCommand, code)
	}
	if len(stdout) == 0 {
		return "", ErrResponse
	}
	return string(stdout), nil
}
func (client Client) ringCommand(ctx context.Context, target Target, args ...string) error {
	stdout, stderr, err := client.sessionRead(ctx, target, args...)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCommand, err)
	}
	_, code := healthBody(stdout, stderr)
	if code != "" {
		return fmt.Errorf("%w: %s", ErrCommand, code)
	}
	return nil
}
func ringWait(ctx context.Context) error {
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func ringGlyph(kind string) string {
	switch kind {
	case "claude":
		return "❯"
	case "codex":
		return "›"
	case "muse":
		return "⟩"
	case "devin":
		return "❭"
	default:
		return ""
	}
}

// bareComposer accepts only one native glyph row in the full visible viewport.
// Other rows can contain a footer, overlay, or a second draft and remain unproven.
func bareComposer(kind, screen string) (string, bool) {
	glyph := ringGlyph(kind)
	if glyph == "" {
		return "", false
	}
	raw, typed, err := ringStripANSI(screen)
	if err != nil {
		return "", false
	}
	rawLines := strings.Split(raw, "\n")
	typedLines := strings.Split(typed, "\n")
	selected := -1
	for i, line := range rawLines {
		if strings.TrimSpace(strings.ReplaceAll(line, "\u2063", "")) == "" {
			continue
		}
		if selected != -1 {
			return "", false
		}
		selected = i
	}
	if selected < 0 {
		return "", false
	}
	rawRow := strings.TrimSpace(strings.ReplaceAll(rawLines[selected], "\u2063", ""))
	typedRow := strings.TrimSpace(strings.ReplaceAll(typedLines[selected], "\u2063", ""))
	if !strings.HasPrefix(rawRow, glyph) || !strings.HasPrefix(typedRow, glyph) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(typedRow, glyph)), true
}

func ringStripANSI(screen string) (string, string, error) {
	var raw, typed strings.Builder
	dim := false
	for i := 0; i < len(screen); {
		if screen[i] == 0x1b {
			if i+2 >= len(screen) || screen[i+1] != '[' {
				return "", "", ErrResponse
			}
			end := strings.IndexByte(screen[i+2:], 'm')
			if end < 0 {
				return "", "", ErrResponse
			}
			code := screen[i+2 : i+2+end]
			for _, token := range strings.Split(code, ";") {
				if token == "" {
					token = "0"
				}
				number, err := strconv.Atoi(token)
				if err != nil {
					return "", "", ErrResponse
				}
				switch number {
				case 0:
					dim = false
				case 2:
					dim = true
				case 22:
					dim = false
				case 1, 4, 39: // Styling does not hide typed content.
				default:
					if (number < 30 || number > 37) && (number < 90 || number > 97) {
						return "", "", ErrResponse
					}
				}
			}
			i += end + 3
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(screen[i:])
		if runeValue == utf8.RuneError && size == 1 {
			return "", "", ErrResponse
		}
		if runeValue < 32 && runeValue != '\n' && runeValue != '\t' {
			return "", "", ErrResponse
		}
		raw.WriteRune(runeValue)
		if !dim || runeValue == '\n' {
			typed.WriteRune(runeValue)
		}
		i += size
	}
	return raw.String(), typed.String(), nil
}
