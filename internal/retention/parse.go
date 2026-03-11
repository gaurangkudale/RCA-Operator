package retention

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultIncidentRetention = 30 * 24 * time.Hour

// ParseIncidentRetention parses incident retention from spec values.
// Preferred value is incidentRetention (e.g. "5m", "2h", "30d").
// If empty, the deprecated incidentRetentionDays field is used as fallback.
func ParseIncidentRetention(incidentRetention string, incidentRetentionDays int) (time.Duration, error) {
	trimmed := strings.TrimSpace(incidentRetention)
	if trimmed != "" {
		return parseRetentionDuration(trimmed)
	}

	if incidentRetentionDays > 0 {
		return time.Duration(incidentRetentionDays) * 24 * time.Hour, nil
	}

	return defaultIncidentRetention, nil
}

func parseRetentionDuration(in string) (time.Duration, error) {
	if len(in) < 2 {
		return 0, fmt.Errorf("invalid incidentRetention %q: expected format like 5m, 2h, 30d", in)
	}

	unit := in[len(in)-1]
	num := in[:len(in)-1]
	value, err := strconv.Atoi(num)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid incidentRetention %q: expected positive integer value", in)
	}

	switch unit {
	case 'm':
		return time.Duration(value) * time.Minute, nil
	case 'h':
		return time.Duration(value) * time.Hour, nil
	case 'd':
		return time.Duration(value) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("invalid incidentRetention %q: supported suffixes are m, h, d", in)
	}
}
