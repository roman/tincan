package firstmate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// SweepReport records one task's result without stopping checks for other tasks.
type SweepReport struct {
	Task   string
	Result CheckResult
	Err    error
}

func regularMetadataEntry(entry os.DirEntry) (bool, error) {
	info, err := entry.Info()
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// Sweep checks each local task metadata entry once in name order.
func Sweep(
	ctx context.Context,
	stateDir AbsolutePath,
	now time.Time,
	check func(context.Context, TaskID, time.Time) (CheckResult, error),
) ([]SweepReport, error) {
	if stateDir.value == "" {
		return nil, ErrInvalidPath
	}
	if check == nil {
		return nil, errors.New("nil task checker")
	}
	entries, err := os.ReadDir(stateDir.value)
	if err != nil {
		return nil, err
	}
	reports := make([]SweepReport, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return reports, err
		}
		if !strings.HasSuffix(entry.Name(), ".meta") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".meta")
		report := SweepReport{Task: name}
		id, err := ParseTaskID(name)
		if err != nil {
			report.Err = err
		} else {
			regular, err := regularMetadataEntry(entry)
			switch {
			case err != nil:
				report.Err = err
			case !regular:
				report.Err = fmt.Errorf("%w: metadata file is not regular", ErrInvalidMetadata)
			default:
				report.Result, report.Err = check(ctx, id, now)
			}
		}
		reports = append(reports, report)
	}
	return reports, nil
}
