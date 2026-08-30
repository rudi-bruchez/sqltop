package model

// DashboardGroup is one folding section of the dashboard: an identifier the
// configuration file uses, a heading, and the figures it holds in the order
// they appear.
type DashboardGroup struct {
	ID      string
	Title   string
	Figures []DashboardFigure
}

// DashboardFigure names one tile. Key is the key in ServerSample.Figures;
// Label is what the tile is called on screen.
type DashboardFigure struct {
	Key   string
	Label string
}

// DashboardCatalogue is every tile the dashboard can show, in the order it
// shows them. Spec section 6, read in the order you read a misbehaving
// server: cpu, then memory, then arriving work, then what is holding on.
//
// It lives in Go rather than only in the browser because the configuration
// file has to be able to list every tile with an explicit on or off, so a
// user can choose without knowing the names by heart. That makes this the
// single source of truth for what tiles exist, and
// internal/web/dashboard_catalogue_test.go checks the shipped app.js against
// it rather than letting the two drift.
var DashboardCatalogue = []DashboardGroup{
	{ID: "cpu", Title: "cpu and schedulers", Figures: []DashboardFigure{
		{"sql_cpu_percent", "sql server cpu"},
		{"other_cpu_percent", "other processes"},
		{"runnable_tasks", "runnable tasks"},
		{"current_tasks", "current tasks"},
		{"scheduler_load_factor", "load factor"},
		{"schedulers_online", "schedulers"},
	}},
	{ID: "memory", Title: "memory", Figures: []DashboardFigure{
		{"total_server_memory_kb", "total server memory"},
		{"target_server_memory_kb", "target server memory"},
		{"buffer_pool_mb", "buffer pool"},
		{"plan_cache_mb", "plan cache"},
		{"query_memory_mb", "query memory"},
		{"page_life_expectancy", "page life expectancy"},
		{"buffer_cache_hit_ratio", "cache hit ratio"},
		{"memory_grants_pending", "grants pending"},
		{"memory_grants_outstanding", "grants outstanding"},
	}},
	{ID: "throughput", Title: "throughput", Figures: []DashboardFigure{
		{"active_requests", "active requests"},
		{"batch_requests_sec", "batch requests"},
		{"compilations_sec", "compilations"},
		{"recompilations_sec", "recompilations"},
		{"full_scans_sec", "full scans"},
		{"page_reads_sec", "page reads"},
		{"page_writes_sec", "page writes"},
		{"lazy_writes_sec", "lazy writes"},
	}},
	{ID: "tempdb", Title: "transactions and tempdb", Figures: []DashboardFigure{
		{"open_transactions", "open transactions"},
		{"longest_transaction_s", "longest transaction"},
		{"tempdb_used_mb", "tempdb used"},
		{"tempdb_free_mb", "tempdb free"},
		{"tempdb_user_objects_mb", "tempdb user objects"},
		{"tempdb_internal_objects_mb", "tempdb internal"},
		{"tempdb_version_store_mb", "tempdb version store"},
		{"version_store_mb", "version store"},
		{"version_store_growth_mb_s", "version store growth"},
	}},
}
