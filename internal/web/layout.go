package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rudi-bruchez/sqltop/internal/config"
	"github.com/rudi-bruchez/sqltop/internal/model"
)

// maxLayoutBody bounds what the layout endpoint will read. A layout is a
// couple of dozen columns; anything past this is not one.
const maxLayoutBody = 64 << 10

// layoutRequest is what the interface posts when the user saves a layout:
// one view, and every column of it in display order with its switch and its
// width. Every column is sent, not only the visible ones, because the order
// of the hidden ones is part of what was saved.
type layoutRequest struct {
	View    string         `json:"view"`
	Columns []layoutColumn `json:"columns"`
}

type layoutColumn struct {
	Field string `json:"field"`
	Show  bool   `json:"show"`
	Width int    `json:"width"`
}

// layout writes the posted column selection back to sqltop.yaml. Spec
// section 8.2: persistence is the configuration file, not browser local
// storage, so it survives a change of browser and can be handed to a
// colleague.
//
// Everything it accepts is checked against the catalogue before it is
// written: a field the catalogue does not know is refused rather than
// stored, and the assembled configuration goes through config.Validate
// exactly as a hand-edited file does. The endpoint holds the token like
// every other route, so this is not the trust boundary; it is there so a
// stale interface cannot quietly corrupt the file that outlives it.
func (s *Server) layout(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var in layoutRequest
	if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, maxLayoutBody)).Decode(&in); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.cfg
	if cfg.Layouts == nil {
		cfg.Layouts = map[string]config.Layout{}
	}
	l := cfg.Layouts["default"]
	if l.Views == nil {
		l.Views = map[string]config.ViewLayout{}
	}
	cols, err := columnLayout(in)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	v := l.Views[in.View]
	v.Columns = cols
	l.Views[in.View] = v
	cfg.Layouts["default"] = l

	if err := cfg.Validate(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	path, err := config.Save(cfg)
	if err != nil {
		http.Error(rw, "could not write the configuration file", http.StatusInternalServerError)
		return
	}

	s.cfg = cfg
	s.grid = resolveAllGrids(cfg)
	writeJSON(rw, map[string]string{"path": path})
}

// columnLayout turns the posted list into what the file stores, refusing
// anything the catalogue does not know and any column named twice.
func columnLayout(in layoutRequest) ([]config.ViewColumn, error) {
	def, known := model.ViewByID(in.View)
	if !known {
		return nil, fmt.Errorf("unknown view %q", in.View)
	}
	valid := map[string]bool{}
	for _, c := range def.Columns {
		valid[c.Field] = true
	}

	out := make([]config.ViewColumn, 0, len(in.Columns))
	seen := map[string]bool{}
	for _, c := range in.Columns {
		if !valid[c.Field] {
			return nil, fmt.Errorf("unknown column %q in view %q", c.Field, in.View)
		}
		if seen[c.Field] {
			return nil, fmt.Errorf("column %q listed twice", c.Field)
		}
		seen[c.Field] = true
		show := c.Show
		out = append(out, config.ViewColumn{Field: c.Field, Show: &show, Width: c.Width})
	}
	return out, nil
}
