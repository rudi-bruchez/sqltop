// Package fake is an in-memory Source, so the collector, window and web
// layers can be tested without a database.
package fake

import (
	"context"
	"sync"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

type Source struct {
	mu   sync.Mutex
	rows []model.RequestSample

	Caps        model.Capabilities
	Info        model.ServerInfo
	CostPerCall int64
	Err         error
	// Figures is what SampleServer hands back. Empty by default, because
	// most callers only care about the grid; the dashboard tests set it.
	Figures map[string]model.Figure
	// AlternateFigure, when set, names a key whose availability flips on
	// every SampleServer call. It exists so a test can watch a tile go from
	// a real reading to no reading and back, which is the only way to
	// catch a page that quietly keeps showing the last value it had. A
	// figure that is never available cannot demonstrate that, and a figure
	// that is always available cannot either.
	AlternateFigure string

	// What the on-demand views of spec section 7 hand back. Nil by
	// default: most tests are about the grid.
	SessionRows []model.SessionSample
	TranRows    []model.TransactionSample
	LockRows    []model.LockSample
	LogRows     []model.LogSpaceSample
	PlanRows    []model.PlanNode
	WaitRows    []model.SessionWait

	cost    model.Cost
	flipped bool
}

func New(rows []model.RequestSample) *Source {
	return &Source{
		rows: rows,
		Caps: model.Caps(model.CapInstanceWideView, model.CapLivePlanProgress),
		Info: model.ServerInfo{Instance: "fake", MajorVersion: 15},
	}
}

// SetRows replaces what the next sample returns, so a test can make the
// population change between ticks.
func (s *Source) SetRows(rows []model.RequestSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = rows
}

func (s *Source) Open(context.Context, string) error { return s.Err }
func (s *Source) Close() error                       { return nil }

func (s *Source) Identify(context.Context) (model.ServerInfo, model.Capabilities, error) {
	return s.Info, s.Caps, s.Err
}

func (s *Source) SampleRequests(context.Context) ([]model.RequestSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	s.cost.CPUMs += s.CostPerCall
	s.cost.At = time.Now()
	out := make([]model.RequestSample, len(s.rows))
	copy(out, s.rows)
	return out, nil
}

func (s *Source) SampleServer(context.Context, model.Tier) (model.ServerSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return model.ServerSample{}, s.Err
	}
	s.cost.CPUMs += s.CostPerCall
	out := make(map[string]model.Figure, len(s.Figures)+1)
	for k, v := range s.Figures {
		out[k] = v
	}
	if s.AlternateFigure != "" {
		s.flipped = !s.flipped
		out[s.AlternateFigure] = model.Figure{Value: 99.5, Unit: "%", Available: s.flipped}
	}
	return model.ServerSample{At: time.Now(), Figures: out}, nil
}

func (s *Source) Cost(context.Context) (model.Cost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cost
	c.At = time.Now()
	return c, nil
}

func (s *Source) QueryText(context.Context, model.RequestRef) (string, error) {
	return "SELECT 1", s.Err
}

func (s *Source) Plan(_ context.Context, _ model.RequestRef, live bool) (model.Plan, error) {
	return model.Plan{Format: "showplan-xml", Payload: []byte("<ShowPlanXML/>"), Live: live}, s.Err
}

// The on-demand views. A fake returns whatever a test put in it, which for
// most tests is nothing: they exercise the grid, not the session list.
func (s *Source) Sessions(context.Context) ([]model.SessionSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SessionRows, s.Err
}

func (s *Source) Transactions(context.Context) ([]model.TransactionSample, []model.LockSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TranRows, s.LockRows, s.Err
}

func (s *Source) LogSpace(context.Context) ([]model.LogSpaceSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LogRows, s.Err
}

func (s *Source) PlanProgress(context.Context, model.RequestRef) ([]model.PlanNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PlanRows, s.Err
}

func (s *Source) SessionWaits(context.Context, int64) ([]model.SessionWait, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.WaitRows, s.Err
}
