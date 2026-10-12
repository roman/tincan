package taskstate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maximumLegacyBacklogBytes = 16 << 20

var legacyQueuedRow = regexp.MustCompile(`^- \[ \] ([A-Za-z0-9][A-Za-z0-9._-]*) - (.*)$`)
var legacyDoneRow = regexp.MustCompile(`^- \[x\] ([A-Za-z0-9][A-Za-z0-9._-]*) - (.*)$`)
var legacyRunningRow = regexp.MustCompile(`^- \*\*([A-Za-z0-9][A-Za-z0-9._-]*)\*\* - (.*)$`)
var legacyHeader = regexp.MustCompile(`^##\s+`)

var legacyDependency = regexp.MustCompile(
	`^(blocked-by|parent|discovered-from):\s*([A-Za-z0-9][A-Za-z0-9._-]*)(?:\s+-\s+(.+))?$`,
)
var legacyDependencyMarker = regexp.MustCompile(`(?:blocked-by|parent|discovered-from):`)
var legacyNextDependency = regexp.MustCompile(`\s+(?:blocked-by|parent|discovered-from):\s`)
var legacyUnparsedField = regexp.MustCompile(
	`\s*\((?:repo:|kind:|priority:|since |done |reported |merged |closed |hold:|hold-kind:|hold-until:)[^()]*\)$`,
)

var legacyTags = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"repo", regexp.MustCompile(`\s*\((?:[^()]*\+\s*)?repo:\s*([^)]+)\)$`)},
	{"kind", regexp.MustCompile(`\s*\(kind:\s*([^)]+)\)$`)},
	{"priority", regexp.MustCompile(`\s*\(priority:\s*([0-4])\)$`)},
	{"created", regexp.MustCompile(`\s*\(since\s+(\d{4}-\d{2}-\d{2})\)$`)},
	{"closed", regexp.MustCompile(`\s*\((?:merged|reported|done|closed)\s+(\d{4}-\d{2}-\d{2})\)$`)},
	{"hold", regexp.MustCompile(`\s*\(hold:\s*([^()]+)\)$`)},
	{"hold-kind", regexp.MustCompile(`\s*\(hold-kind:\s*(captain|external|load|parked|future)\)$`)},
	{"hold-until", regexp.MustCompile(`\s*\(hold-until:\s*(\d{4}-\d{2}-\d{2})\)$`)},
}

// LegacyBacklogRow holds the task fields before worker records are joined.
type LegacyBacklogRow struct {
	Definition Definition
	State      State
}

// ParseLegacyBacklog reads all version 1 rows and rejects unconvertible data.
func ParseLegacyBacklog(data []byte) ([]LegacyBacklogRow, error) {
	if len(data) > maximumLegacyBacklogBytes || !utf8.Valid(data) ||
		strings.ContainsRune(string(data), 0) {
		return nil, fmt.Errorf("invalid legacy backlog bytes")
	}
	rows := []LegacyBacklogRow{}
	seen := map[string]bool{}
	var state State
	var body []string
	finish := func() error {
		if len(rows) == 0 || body == nil {
			return nil
		}
		for len(body) > 0 && body[len(body)-1] == "" {
			body = body[:len(body)-1]
		}
		rows[len(rows)-1].Definition.Body = strings.Join(body, "\n")
		body = nil
		if !validDefinition(rows[len(rows)-1].Definition) {
			return fmt.Errorf("invalid legacy task %q", rows[len(rows)-1].Definition.ID)
		}
		return nil
	}
	for lineNumber, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.Contains(line, "tasks-axi:public-followup") {
			return nil, fmt.Errorf("unsupported legacy task metadata at line %d", lineNumber+1)
		}
		if header := legacyHeader.FindStringIndex(line); header != nil {
			if err := finish(); err != nil {
				return nil, err
			}
			state = legacySectionState(line[header[1]:])
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if err := finish(); err != nil {
				return nil, err
			}
			match := legacyRowMatch(state, line)
			if match == nil {
				return nil, fmt.Errorf("invalid legacy task row at line %d", lineNumber+1)
			}
			definition, err := parseLegacyFields(match[1], match[2])
			if err != nil {
				return nil, fmt.Errorf("legacy task at line %d: %w", lineNumber+1, err)
			}
			if seen[definition.ID] {
				return nil, fmt.Errorf("duplicate legacy task %q", definition.ID)
			}
			seen[definition.ID] = true
			rows = append(rows, LegacyBacklogRow{Definition: definition, State: state})
			body = []string{}
			continue
		}
		if strings.HasPrefix(line, "  ") {
			if body == nil {
				return nil, fmt.Errorf("orphan legacy task body at line %d", lineNumber+1)
			}
			body = append(body, line[2:])
			continue
		}
		if strings.TrimSpace(line) == "" {
			if body != nil {
				body = append(body, "")
			}
			continue
		}
		if state != "" {
			return nil, fmt.Errorf("unexpected legacy backlog content at line %d", lineNumber+1)
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return rows, nil
}

func legacySectionState(header string) State {
	name := strings.ToLower(strings.TrimSpace(header))
	switch {
	case name == "queued":
		return Queued
	case name == "in flight":
		return InFlight
	case strings.HasPrefix(name, "done"):
		return Done
	default:
		return ""
	}
}

func legacyRowMatch(state State, line string) []string {
	switch state {
	case Queued:
		return legacyQueuedRow.FindStringSubmatch(line)
	case InFlight:
		if match := legacyRunningRow.FindStringSubmatch(line); match != nil {
			return match
		}
		return legacyQueuedRow.FindStringSubmatch(line)
	case Done:
		return legacyDoneRow.FindStringSubmatch(line)
	default:
		return nil
	}
}

func parseLegacyFields(id, prose string) (Definition, error) {
	prose = strings.TrimRight(prose, " \t")
	definition := Definition{ID: id}
	seenTags := map[string]bool{}
	for {
		if prefix, dependency, found := stripLegacyDependency(prose); found {
			definition.Dependencies = append([]Dependency{dependency}, definition.Dependencies...)
			prose = prefix
			continue
		}
		matched := false
		for _, tag := range legacyTags {
			positions := tag.pattern.FindStringSubmatchIndex(prose)
			if positions == nil {
				continue
			}
			if seenTags[tag.name] {
				return Definition{}, fmt.Errorf("duplicate %s tag", tag.name)
			}
			seenTags[tag.name] = true
			value := strings.TrimSpace(prose[positions[2]:positions[3]])
			if err := applyLegacyTag(&definition, tag.name, value); err != nil {
				return Definition{}, err
			}
			prose = strings.TrimRight(prose[:positions[0]], " \t")
			matched = true
			break
		}
		if !matched {
			break
		}
	}
	definition.Title = strings.TrimSpace(prose)
	if legacyUnparsedField.MatchString(definition.Title) {
		return Definition{}, fmt.Errorf("unparsed legacy task field")
	}
	if definition.Kind == "" {
		definition.Kind = legacyLeadingKind(definition.Title)
	}
	if definition.Kind == "public-followup" {
		return Definition{}, fmt.Errorf("public follow-up metadata needs a migration format")
	}
	if definition.Hold != nil && definition.Hold.Reason == "" {
		return Definition{}, fmt.Errorf("hold details lack a reason")
	}
	if !validDefinition(definition) {
		return Definition{}, fmt.Errorf("invalid legacy task fields")
	}
	return definition, nil
}

func stripLegacyDependency(prose string) (string, Dependency, bool) {
	for _, position := range legacyDependencyMarker.FindAllStringIndex(prose, -1) {
		match := legacyDependency.FindStringSubmatch(prose[position[0]:])
		if match == nil || legacyNextDependency.MatchString(match[3]) {
			continue
		}
		return strings.TrimRight(prose[:position[0]], " \t"), Dependency{
			Type: match[1], ID: match[2], Reason: match[3],
		}, true
	}
	return prose, Dependency{}, false
}

func applyLegacyTag(definition *Definition, name, value string) error {
	switch name {
	case "repo":
		definition.Repo = value
	case "kind":
		definition.Kind = value
	case "priority":
		priority := int(value[0] - '0')
		definition.Priority = &priority
	case "created":
		definition.Created = value
	case "closed":
		definition.Closed = value
	case "hold":
		if definition.Hold == nil {
			definition.Hold = &Hold{}
		}
		definition.Hold.Reason = value
	case "hold-kind":
		if definition.Hold == nil {
			definition.Hold = &Hold{}
		}
		definition.Hold.Kind = value
	case "hold-until":
		if definition.Hold == nil {
			definition.Hold = &Hold{}
		}
		definition.Hold.Until = value
	default:
		return fmt.Errorf("unsupported legacy tag %q", name)
	}
	return nil
}

func legacyLeadingKind(title string) string {
	for _, candidate := range []struct{ prefix, kind string }{
		{"PERSISTENT SECONDMATE", "secondmate"},
		{"SHIP", "ship"},
		{"SCOUT", "scout"},
		{"DOCS-ONLY", "docs"},
	} {
		if strings.HasPrefix(title, candidate.prefix) &&
			(len(title) == len(candidate.prefix) || !legacyWordByte(title[len(candidate.prefix)])) {
			return candidate.kind
		}
	}
	return ""
}

func legacyWordByte(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' || value == '_'
}
