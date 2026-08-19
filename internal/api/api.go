// Package api is the control plane: the VERBS. The CLI and any future web UI
// are thin clients of these routes — if a UI can do something this API can't,
// that is a bug, because CI only ever sees the API.
package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/luum-ev/ocpp-lab/internal/fleet"
	"github.com/luum-ev/ocpp-lab/internal/station"
)

// The web UI ships inside the binary — one file, no build step, and it can
// only do what the API can do, because it IS the API's client.
//
//go:embed web/index.html
var webUI embed.FS

type Server struct {
	Fleet *fleet.Fleet
	Log   *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Liveness for Kubernetes and container healthchecks — distroless has no
	// shell, so health is HTTP or nothing.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page, err := webUI.ReadFile("web/index.html")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /stations", s.listStations)
	mux.HandleFunc("POST /stations/{id}/connectors/{connector}/plug", s.connectorAction("plug"))
	mux.HandleFunc("POST /stations/{id}/connectors/{connector}/unplug", s.connectorAction("unplug"))
	mux.HandleFunc("POST /stations/{id}/connectors/{connector}/charge", s.charge)
	mux.HandleFunc("POST /stations/{id}/connectors/{connector}/stop", s.connectorAction("stop"))
	mux.HandleFunc("POST /stations/{id}/connectors/{connector}/fault", s.fault)
	mux.HandleFunc("POST /stations/{id}/kill", s.stationAction("kill"))
	mux.HandleFunc("POST /stations/{id}/offline", s.stationAction("offline"))
	mux.HandleFunc("POST /stations/{id}/online", s.stationAction("online"))
	return mux
}

func (s *Server) listStations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Fleet.Snapshots())
}

func (s *Server) station(w http.ResponseWriter, r *http.Request) (*station.Station, bool) {
	st, ok := s.Fleet.Station(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("station %q not found", r.PathValue("id")))
	}
	return st, ok
}

func (s *Server) connector(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("connector"))
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("connector must be a positive integer"))
		return 0, false
	}
	return n, true
}

func (s *Server) connectorAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, ok := s.station(w, r)
		if !ok {
			return
		}
		n, ok := s.connector(w, r)
		if !ok {
			return
		}
		var err error
		switch action {
		case "plug":
			err = st.Plug(n)
		case "unplug":
			err = st.Unplug(n)
		case "stop":
			err = st.StopCharge(n, "Local")
		}
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, st.Snapshot())
	}
}

type chargeRequest struct {
	IDTag string `json:"idTag"`
	// Battery overrides the station's default simulated EV for this session.
	Battery *station.EVBattery `json:"battery,omitempty"`
}

func (s *Server) charge(w http.ResponseWriter, r *http.Request) {
	st, ok := s.station(w, r)
	if !ok {
		return
	}
	n, ok := s.connector(w, r)
	if !ok {
		return
	}
	var req chargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !strings.Contains(err.Error(), "EOF") {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.IDTag == "" {
		req.IDTag = "ocpp-lab"
	}
	if err := st.StartCharge(n, req.IDTag, req.Battery); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, st.Snapshot())
}

type faultRequest struct {
	ErrorCode string `json:"errorCode"`
}

func (s *Server) fault(w http.ResponseWriter, r *http.Request) {
	st, ok := s.station(w, r)
	if !ok {
		return
	}
	n, ok := s.connector(w, r)
	if !ok {
		return
	}
	var req faultRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ErrorCode == "" {
		req.ErrorCode = "OtherError"
	}
	if err := st.Fault(n, req.ErrorCode); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, st.Snapshot())
}

func (s *Server) stationAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, ok := s.station(w, r)
		if !ok {
			return
		}
		switch action {
		case "kill":
			st.Kill()
		case "offline":
			st.Offline()
		case "online":
			st.Online()
		}
		writeJSON(w, http.StatusOK, st.Snapshot())
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
