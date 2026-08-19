// Package fleet loads the declarative fleet file and owns station lifecycle.
// The split matters: fleet.yaml holds NOUNS (which stations exist), the API
// holds VERBS (plug, charge, kill) — desired state vs. runtime events.
package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/luum-ev/ocpp-lab/internal/station"
)

type File struct {
	CSMS     string           `yaml:"csms"`
	Stations []station.Config `yaml:"stations"`
}

type Fleet struct {
	CSMS string

	mu       sync.RWMutex
	stations map[string]*station.Station
	log      *slog.Logger
}

// Load reads the fleet file. csmsOverride, when non-empty, replaces the
// file's csms URL — the fleet file stays generic (a ConfigMap shared by every
// environment) and the endpoint comes from the environment.
func Load(path string, csmsOverride string, log *slog.Logger) (*Fleet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fleet file: %w", err)
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse fleet file: %w", err)
	}
	if csmsOverride != "" {
		f.CSMS = csmsOverride
	}
	if f.CSMS == "" {
		return nil, fmt.Errorf("fleet file: csms URL is required (set it in the file or via OCPP_LAB_CSMS)")
	}
	fl := &Fleet{CSMS: f.CSMS, stations: map[string]*station.Station{}, log: log}
	for _, cfg := range f.Stations {
		if cfg.ID == "" {
			return nil, fmt.Errorf("fleet file: every station needs an id")
		}
		if _, dup := fl.stations[cfg.ID]; dup {
			return nil, fmt.Errorf("fleet file: duplicated station id %q", cfg.ID)
		}
		fl.stations[cfg.ID] = station.New(cfg, f.CSMS, log)
	}
	return fl, nil
}

// Run starts every station and blocks until ctx ends.
func (f *Fleet) Run(ctx context.Context) {
	var wg sync.WaitGroup
	f.mu.RLock()
	for _, s := range f.stations {
		wg.Add(1)
		go func(s *station.Station) {
			defer wg.Done()
			s.Run(ctx)
		}(s)
	}
	f.mu.RUnlock()
	wg.Wait()
}

func (f *Fleet) Station(id string) (*station.Station, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	s, ok := f.stations[id]
	return s, ok
}

func (f *Fleet) Snapshots() []map[string]any {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ids := make([]string, 0, len(f.stations))
	for id := range f.stations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, f.stations[id].Snapshot())
	}
	return out
}
