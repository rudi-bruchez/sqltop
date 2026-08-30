package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestConfParsesItsDurations exists because this binary had none, and that
// cost a broken demonstration: it borrowed sqltop's config.Duration, sqltop's
// configuration format changed from JSON to YAML, that type stopped speaking
// JSON, and sqlstress.json stopped loading with nothing failing anywhere
// until somebody ran it. A load generator has no business depending on the
// configuration format of the tool it exercises, and now it does not.
func TestConfParsesItsDurations(t *testing.T) {
	var got conf
	body := `{"threads":4,"duration":"90s","pause":"250ms","queries":"q","database":"D"}`
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Threads != 4 || got.Queries != "q" || got.Database != "D" {
		t.Errorf("got %+v", got)
	}
	if got.Duration.Std() != 90*time.Second {
		t.Errorf("duration = %s, want 90s", got.Duration)
	}
	if got.Pause.Std() != 250*time.Millisecond {
		t.Errorf("pause = %s, want 250ms", got.Pause)
	}
}

// TestConfRejectsANonDuration keeps the error useful: a number where a
// duration belongs is the mistake people make, and "60" is not "60s".
func TestConfRejectsANonDuration(t *testing.T) {
	var got conf
	err := json.Unmarshal([]byte(`{"duration":"60"}`), &got)
	if err == nil {
		t.Fatal("\"60\" was accepted as a duration")
	}
	if err = json.Unmarshal([]byte(`{"duration":60}`), &got); err == nil {
		t.Fatal("a bare number was accepted as a duration")
	}
}

// TestShippedConfigLoads is the check that would have caught the break: the
// file in this directory is the one the demonstration uses.
func TestShippedConfigLoads(t *testing.T) {
	cfg, err := loadConf("sqlstress.json")
	if err != nil {
		t.Fatalf("the shipped sqlstress.json does not load: %v", err)
	}
	if cfg.Threads <= 0 || cfg.Duration.Std() <= 0 || cfg.Queries == "" {
		t.Errorf("loaded %+v, which is not a usable configuration", cfg)
	}
}
