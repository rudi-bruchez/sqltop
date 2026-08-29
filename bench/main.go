// Rendering test bench for the sqltop web interface.
//
// It talks to no DBMS: a generator produces a synthetic population of active
// requests that evolves the way it would on a busy server (counters climbing,
// sessions coming and going, blocking chains), and pushes it over SSE at the
// requested rate.
//
// Purpose: measure the refresh cost of the four rendering strategies under
// consideration, and check that scroll position and selection survive.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web
var webFS embed.FS

const histLen = 24 // points kept for the CPU sparkline

// Row mirrors the columns the "Resource Usage" screen will show.
type Row struct {
	SPID      int     `json:"spid"`
	Status    string  `json:"status"`
	DB        string  `json:"db"`
	Login     string  `json:"login"`
	Host      string  `json:"host"`
	Program   string  `json:"program"`
	Command   string  `json:"command"`
	WaitType  string  `json:"wait_type"`
	WaitMs    int     `json:"wait_ms"`
	DurS      int     `json:"dur_s"`
	CPUMs     int     `json:"cpu_ms"`
	ReadsMB   float64 `json:"reads_mb"`
	WritesMB  float64 `json:"writes_mb"`
	TempdbMB  float64 `json:"tempdb_mb"`
	GrantMB   float64 `json:"grant_mb"`
	DOP       int     `json:"dop"`
	BlockedBy int     `json:"blocked_by"`
	Pct       int     `json:"pct"`
	SQL       string  `json:"sql"`
	CPUHist   []int   `json:"cpu_hist"`
}

// Snapshot is the full state, Delta carries only what moved.
type Snapshot struct {
	Seq  uint64 `json:"seq"`
	TS   int64  `json:"ts"`
	Rows []*Row `json:"rows"`
}

type Delta struct {
	Seq    uint64 `json:"seq"`
	TS     int64  `json:"ts"`
	Upsert []*Row `json:"upsert"`
	Remove []int  `json:"remove"`
}

var (
	statuses  = []string{"running", "runnable", "suspended", "sleeping"}
	databases = []string{"CRM", "Sales", "Accounting", "Inventory", "Archive", "tempdb", "Reference"}
	logins    = []string{"app_web", "app_batch", "etl_svc", "reporting", "sa", "dba_rudi", "svc_edi"}
	hosts     = []string{"WEB01", "WEB02", "BATCH01", "ETL03", "BI01", "DESK-142", "APPSRV07"}
	programs  = []string{
		".Net SqlClient Data Provider",
		"Microsoft SQL Server Management Studio - Query",
		"SQLAgent - TSQL JobStep (Job 0x9F2A...)",
		"EntityFramework",
		"Report Server",
		"jTDS",
	}
	commands = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "BACKUP DATABASE", "DBCC"}
	waits    = []string{
		"", "CXPACKET", "PAGEIOLATCH_SH", "LCK_M_X", "WRITELOG", "SOS_SCHEDULER_YIELD",
		"RESOURCE_SEMAPHORE", "ASYNC_NETWORK_IO", "PAGELATCH_UP", "LCK_M_S", "THREADPOOL",
	}
	sqlText = []string{
		"SELECT c.customer_id, c.company_name, SUM(l.net_amount) FROM dbo.SalesOrder c JOIN dbo.SalesOrderLine l ON l.order_id = c.order_id WHERE c.order_date >= @p1 GROUP BY c.customer_id, c.company_name ORDER BY 3 DESC",
		"UPDATE dbo.Inventory SET quantity = quantity - @p1 WHERE item_id = @p2 AND warehouse_id = @p3",
		"INSERT INTO dbo.EventLog (logged_at, severity, source, message) VALUES (SYSUTCDATETIME(), @p1, @p2, @p3)",
		"EXEC dbo.usp_MonthlyConsolidation @year = @p1, @month = @p2",
		"SELECT TOP (1000) * FROM dbo.JournalEntry WITH (NOLOCK) WHERE fiscal_year = @p1 AND account LIKE @p2 + '%'",
		"DELETE FROM dbo.WebSession WHERE last_activity < DATEADD(hour, -2, GETDATE())",
		"MERGE dbo.DimCustomer AS target USING stg.Customer AS source ON target.code = source.code WHEN MATCHED THEN UPDATE SET target.label = source.label WHEN NOT MATCHED THEN INSERT (code, label) VALUES (source.code, source.label)",
	}
)

// World holds the current population and advances it from tick to tick.
type World struct {
	mu       sync.Mutex
	rows     map[int]*Row
	order    []int // insertion order, for a stable display
	nextSPID int
	seq      uint64
	target   int // desired number of rows
	churnPct int // percentage of rows recycled on each tick
	rnd      *rand.Rand
}

func newWorld(target, churnPct int) *World {
	w := &World{
		rows:     make(map[int]*Row),
		nextSPID: 51, // user spids start above the system sessions
		target:   target,
		churnPct: churnPct,
		rnd:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	w.adjustPopulation()
	return w
}

func (w *World) pick(s []string) string { return s[w.rnd.Intn(len(s))] }

func (w *World) newRow() *Row {
	spid := w.nextSPID
	w.nextSPID++
	return &Row{
		SPID:     spid,
		Status:   w.pick(statuses),
		DB:       w.pick(databases),
		Login:    w.pick(logins),
		Host:     w.pick(hosts),
		Program:  w.pick(programs),
		Command:  w.pick(commands),
		WaitType: w.pick(waits),
		DOP:      1 + w.rnd.Intn(8),
		SQL:      w.pick(sqlText),
		CPUHist:  make([]int, histLen),
		GrantMB:  float64(w.rnd.Intn(2048)),
	}
}

// adjustPopulation brings the population back to target. Called under lock.
func (w *World) adjustPopulation() {
	for len(w.rows) < w.target {
		r := w.newRow()
		w.rows[r.SPID] = r
		w.order = append(w.order, r.SPID)
	}
	for len(w.rows) > w.target && len(w.order) > 0 {
		spid := w.order[0]
		w.order = w.order[1:]
		delete(w.rows, spid)
	}
}

// tick advances the simulation one step and returns the matching payloads.
func (w *World) tick() (*Snapshot, *Delta) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.seq++
	var removed []int

	// Recycling: a few sessions end, as many start.
	n := len(w.order) * w.churnPct / 100
	for i := 0; i < n && len(w.order) > 0; i++ {
		idx := w.rnd.Intn(len(w.order))
		spid := w.order[idx]
		w.order = append(w.order[:idx], w.order[idx+1:]...)
		delete(w.rows, spid)
		removed = append(removed, spid)
	}
	w.adjustPopulation()

	// Counters move on. On a real server nearly every active row changes every
	// second, so the delta saves nothing on the wire: it can only save DOM work.
	for _, r := range w.rows {
		r.DurS++
		burst := w.rnd.Intn(900)
		r.CPUMs += burst
		r.ReadsMB += w.rnd.Float64() * 40
		r.WritesMB += w.rnd.Float64() * 6
		r.TempdbMB += w.rnd.Float64() * 3
		r.CPUHist = append(r.CPUHist[1:], burst)

		if w.rnd.Intn(4) == 0 {
			r.Status = w.pick(statuses)
			r.WaitType = w.pick(waits)
		}
		if r.WaitType == "" {
			r.WaitMs = 0
		} else {
			r.WaitMs += w.rnd.Intn(1200)
		}
		if r.Pct > 0 || w.rnd.Intn(20) == 0 {
			r.Pct = (r.Pct + 1 + w.rnd.Intn(4)) % 101
		}
		r.BlockedBy = 0
	}

	// Blocking chains: roughly one row in eight is blocked by another, which
	// yields a tree two or three levels deep.
	for _, spid := range w.order {
		if w.rnd.Intn(8) != 0 {
			continue
		}
		blocker := w.order[w.rnd.Intn(len(w.order))]
		if blocker == spid {
			continue
		}
		// Cycles are avoided by only ever blocking on a lower spid.
		if blocker > spid {
			continue
		}
		w.rows[spid].BlockedBy = blocker
		w.rows[spid].Status = "suspended"
		if w.rows[spid].WaitType == "" {
			w.rows[spid].WaitType = "LCK_M_X"
		}
	}

	rows := w.currentRows()
	now := time.Now().UnixMilli()
	return &Snapshot{Seq: w.seq, TS: now, Rows: rows},
		&Delta{Seq: w.seq, TS: now, Upsert: rows, Remove: removed}
}

// currentRows copies the population in display order. Called under lock.
func (w *World) currentRows() []*Row {
	rows := make([]*Row, 0, len(w.order))
	for _, spid := range w.order {
		if r, ok := w.rows[spid]; ok {
			rows = append(rows, r.clone())
		}
	}
	return rows
}

func (r *Row) clone() *Row {
	c := *r
	c.CPUHist = append([]int(nil), r.CPUHist...)
	return &c
}

// snapshot returns the current state without advancing the simulation.
func (w *World) snapshot() *Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return &Snapshot{Seq: w.seq, TS: time.Now().UnixMilli(), Rows: w.currentRows()}
}

func (w *World) setTarget(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.target = n
	w.adjustPopulation()
}

func (w *World) setChurn(p int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.churnPct = p
}

// hub broadcasts each tick to the SSE subscribers.
type hub struct {
	mu   sync.Mutex
	subs map[*sub]struct{}
}

type sub struct {
	ch   chan []byte
	feed string // "snapshot" or "delta"
}

func newHub() *hub { return &hub{subs: make(map[*sub]struct{})} }

func (h *hub) add(s *sub) {
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(s *sub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
	close(s.ch)
}

var dropped atomic.Int64

func (h *hub) broadcast(snapMsg, deltaMsg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		msg := snapMsg
		if s.feed == "delta" {
			msg = deltaMsg
		}
		select {
		case s.ch <- msg:
		default:
			// The browser cannot keep up: drop rather than stall the tick.
			dropped.Add(1)
		}
	}
}

func sseEvent(name string, payload any) []byte {
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("marshal %s: %v", name, err)
		return nil
	}
	return []byte("event: " + name + "\ndata: " + string(b) + "\n\n")
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8420", "listen address")
	rows := flag.Int("rows", 300, "number of simulated active requests")
	hz := flag.Float64("hz", 1, "refresh rate in Hz")
	churn := flag.Int("churn", 5, "percentage of sessions recycled on each tick")
	flag.Parse()

	world := newWorld(*rows, *churn)
	h := newHub()

	var tickInterval atomic.Uint64
	tickInterval.Store(uint64(time.Duration(float64(time.Second) / *hz)))

	go func() {
		for {
			time.Sleep(time.Duration(tickInterval.Load()))
			snap, delta := world.tick()
			h.broadcast(sseEvent("snapshot", snap), sseEvent("delta", delta))
		}
	}()

	content, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(content)))

	mux.HandleFunc("/stream", func(rw http.ResponseWriter, req *http.Request) {
		flusher, ok := rw.(http.Flusher)
		if !ok {
			http.Error(rw, "streaming not supported", http.StatusInternalServerError)
			return
		}
		feed := req.URL.Query().Get("feed")
		if feed != "delta" {
			feed = "snapshot"
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("Cache-Control", "no-cache")
		rw.Header().Set("Connection", "keep-alive")
		rw.Header().Set("X-Accel-Buffering", "no")

		s := &sub{ch: make(chan []byte, 4), feed: feed}
		h.add(s)
		defer h.remove(s)

		// First send is always a full state, delta mode included.
		if _, err := rw.Write(sseEvent("snapshot", world.snapshot())); err != nil {
			return
		}
		flusher.Flush()

		for {
			select {
			case <-req.Context().Done():
				return
			case msg, open := <-s.ch:
				if !open {
					return
				}
				if _, err := rw.Write(msg); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})

	// Live steering: the sliders on the page call this endpoint.
	mux.HandleFunc("/control", func(rw http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if v := q.Get("rows"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 20000 {
				world.setTarget(n)
			}
		}
		if v := q.Get("churn"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
				world.setChurn(n)
			}
		}
		if v := q.Get("hz"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 20 {
				tickInterval.Store(uint64(time.Duration(float64(time.Second) / f)))
			}
		}
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(rw, `{"dropped":%d}`, dropped.Load())
	})

	log.Printf("sqltop rendering bench on http://%s (rows=%d hz=%g churn=%d%%)", *addr, *rows, *hz, *churn)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
