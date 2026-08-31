package mssql

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// The ring buffer hands back what it holds on every read, so an event is seen
// many times and has to be placed rather than deduplicated by guesswork.
// totalEventsProcessed is cumulative for the life of the session and
// eventCount is what is held now, so the buffer holds absolute indices
// total-eventCount through total-1, in order, and the document holds a prefix
// of that range: measured on 2022, a truncated read keeps the oldest and
// drops the newest.
type ringTarget struct {
	Truncated  int       `xml:"truncated,attr"`
	Total      int64     `xml:"totalEventsProcessed,attr"`
	EventCount int64     `xml:"eventCount,attr"`
	Events     []ringEvt `xml:"event"`
}

type ringEvt struct {
	Name      string     `xml:"name,attr"`
	Timestamp string     `xml:"timestamp,attr"`
	Data      []ringData `xml:"data"`
	Actions   []ringData `xml:"action"`
}

// Text carries the readable form where the engine has one. For result the
// engine puts the numeric code in value and the wording in text, so a parser
// that reads only value reports every statement as "0".
type ringData struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value"`
	Text  string `xml:"text"`
}

func parseRingBuffer(doc string, mark int64) ([]model.CapturedStatement, model.CaptureProgress, error) {
	if strings.TrimSpace(doc) == "" {
		return nil, model.CaptureProgress{Seen: mark}, nil
	}
	var t ringTarget
	if err := xml.Unmarshal([]byte(doc), &t); err != nil {
		return nil, model.CaptureProgress{Seen: mark}, err
	}

	prog := model.CaptureProgress{Total: t.Total}
	// Either signal means the document is not the whole buffer. The flag is
	// the server saying so; a node count below eventCount says the same and
	// is the one that shows up first.
	prog.Truncated = t.Truncated != 0 || int64(len(t.Events)) < t.EventCount

	// The first node of the document is the oldest event the buffer holds,
	// truncated or not.
	first := t.Total - t.EventCount
	if first < 0 {
		first = 0
	}
	if first > mark {
		prog.Missed = first - mark
	}
	// The mark may only advance to the end of what this document carried.
	prog.Seen = first + int64(len(t.Events))
	if prog.Seen < mark {
		prog.Seen = mark
	}

	out := make([]model.CapturedStatement, 0, len(t.Events))
	for i, e := range t.Events {
		if first+int64(i) < mark {
			continue
		}
		out = append(out, statementOf(e))
	}
	return out, prog, nil
}

func statementOf(e ringEvt) model.CapturedStatement {
	s := model.CapturedStatement{Kind: "batch"}
	if e.Name == "rpc_completed" {
		s.Kind = "rpc"
	}
	if at, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
		s.At = at
	}
	for _, d := range e.Data {
		switch d.Name {
		case "duration":
			s.DurationUs = atoi(d.Value)
		case "cpu_time":
			s.CPUUs = atoi(d.Value)
		case "logical_reads":
			s.LogicalReads = atoi(d.Value)
		case "physical_reads":
			s.PhysicalReads = atoi(d.Value)
		case "writes":
			s.Writes = atoi(d.Value)
		case "row_count":
			s.RowCount = atoi(d.Value)
		case "result":
			s.Result = pick(d.Text, d.Value)
		case "object_name":
			s.Object = pick(d.Text, d.Value)
		case "batch_text", "statement":
			s.Text = d.Value
		}
	}
	for _, a := range e.Actions {
		switch a.Name {
		case "database_name":
			s.Database = pick(a.Text, a.Value)
		case "client_app_name":
			s.Application = pick(a.Text, a.Value)
		case "username":
			s.User = pick(a.Text, a.Value)
		}
	}
	return s
}

func pick(text, value string) string {
	if text != "" {
		return text
	}
	return value
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
