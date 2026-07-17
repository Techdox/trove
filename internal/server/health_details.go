package server

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/techdox/trove/pkg/model"
)

const maxStoredHealthDetailRunes = 512

var (
	healthBearerSecret = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	healthNamedSecret  = regexp.MustCompile(`(?i)\b(authorization|api[_-]?key|password|passwd|secret|token)\s*[:=]\s*[^\s,;]+`)
)

// LoadHealthDetailsEnabledFromEnv returns the explicit opt-in for collecting,
// storing, and exposing platform health messages. The secure default is off.
func LoadHealthDetailsEnabledFromEnv() bool {
	enabled, err := strconv.ParseBool(os.Getenv("TROVE_HEALTH_DETAILS_ENABLED"))
	return err == nil && enabled
}

// ConfigureHealthDetails controls whether optional platform health messages
// are retained and returned by the services API. It is disabled by default.
func (s *Server) ConfigureHealthDetails(enabled bool) {
	s.healthDetailsEnabled = enabled
}

func (s *Server) filterHealthDetails(report *model.Report) {
	for i := range report.Services {
		if !s.healthDetailsEnabled {
			report.Services[i].HealthDetail = ""
			continue
		}
		report.Services[i].HealthDetail = sanitizeHealthDetail(report.Services[i].HealthDetail)
	}
}

func (s *Server) exposedHealthDetail(value string) string {
	if !s.healthDetailsEnabled {
		return ""
	}
	return sanitizeHealthDetail(value)
}

func sanitizeHealthDetail(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = healthBearerSecret.ReplaceAllString(value, "Bearer [REDACTED]")
	value = healthNamedSecret.ReplaceAllStringFunc(value, func(match string) string {
		if i := strings.IndexAny(match, ":="); i >= 0 {
			return strings.TrimSpace(match[:i]) + "=[REDACTED]"
		}
		return "[REDACTED]"
	})
	runes := []rune(value)
	if len(runes) > maxStoredHealthDetailRunes {
		value = string(runes[:maxStoredHealthDetailRunes-1]) + "…"
	}
	return value
}
