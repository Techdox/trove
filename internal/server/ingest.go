package server

import (
	"encoding/json"
	"net/http"

	"github.com/techdox/trove/internal/store"
	"github.com/techdox/trove/pkg/model"
)

// maxReportBytes caps an inbound report body. Generous for hundreds of
// containers, small enough to bound memory from a misbehaving/hostile client.
const maxReportBytes = 8 << 20 // 8 MiB

// handleReport ingests a full-state report from an authenticated agent.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request, agent store.Agent) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReportBytes)

	var report model.Report
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid report body: "+err.Error())
		return
	}
	if err := report.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.store.ApplyReport(r.Context(), agent.ID, &report); err != nil {
		s.log.Error("apply report", "agent", agent.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to store report")
		return
	}

	s.reportsAccepted.Add(1)
	s.log.Info("report accepted",
		"agent", agent.Name, "host", report.Host.Hostname, "services", len(report.Services))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"services": len(report.Services),
	})
}
