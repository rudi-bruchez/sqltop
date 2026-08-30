package mssql

import "testing"

const ringOne = `<RingBufferTarget truncated="0" processingTime="0" totalEventsProcessed="3" eventCount="3" droppedCount="0" memoryUsed="775">
  <event name="sql_batch_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.238Z">
    <data name="duration"><type name="uint64" package="package0"></type><value>1234</value></data>
    <data name="cpu_time"><value>1000</value></data>
    <data name="logical_reads"><value>7</value></data>
    <data name="physical_reads"><value>0</value></data>
    <data name="writes"><value>0</value></data>
    <data name="row_count"><value>1</value></data>
    <data name="result"><type name="rpc_return_result" package="sqlserver"></type><value>0</value><text><![CDATA[OK]]></text></data>
    <data name="batch_text"><type name="unicode_string" package="package0"></type><value><![CDATA[SELECT 1]]></value></data>
    <action name="database_name" package="sqlserver"><value>tempdb</value></action>
    <action name="client_app_name" package="sqlserver"><value>sqlcmd</value></action>
    <action name="username" package="sqlserver"><value>sa</value></action>
  </event>
  <event name="rpc_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.239Z">
    <data name="duration"><value>50</value></data>
    <data name="object_name"><value>sp_executesql</value></data>
    <data name="statement"><value><![CDATA[SELECT @a]]></value></data>
  </event>
  <event name="sql_batch_completed" package="sqlserver" timestamp="2026-08-30T20:19:50.240Z">
    <data name="duration"><value>9</value></data>
    <data name="batch_text"><value><![CDATA[SELECT 2]]></value></data>
  </event>
</RingBufferTarget>`

func TestParseRingBufferReadsFieldsAndActions(t *testing.T) {
	got, prog, err := parseRingBuffer(ringOne, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d statements, want 3", len(got))
	}
	if prog.Total != 3 || prog.Missed != 0 || prog.Truncated {
		t.Errorf("progress %+v, want Total 3 and nothing lost", prog)
	}
	b := got[0]
	if b.Kind != "batch" || b.Text != "SELECT 1" {
		t.Errorf("first statement is %s %q", b.Kind, b.Text)
	}
	if b.DurationUs != 1234 || b.CPUUs != 1000 || b.LogicalReads != 7 || b.RowCount != 1 {
		t.Errorf("numbers came out %+v", b)
	}
	// The field that made this test necessary: <value> is the numeric code.
	if b.Result != "OK" {
		t.Errorf("result is %q, want OK; the engine puts the code in <value> and the wording in <text>", b.Result)
	}
	if b.Database != "tempdb" || b.Application != "sqlcmd" || b.User != "sa" {
		t.Errorf("actions came out database=%q app=%q user=%q", b.Database, b.Application, b.User)
	}
	if b.At.IsZero() {
		t.Error("the timestamp did not parse")
	}
	r := got[1]
	if r.Kind != "rpc" || r.Object != "sp_executesql" || r.Text != "SELECT @a" {
		t.Errorf("rpc came out %s object=%q text=%q", r.Kind, r.Object, r.Text)
	}
}

func TestParseRingBufferEmitsOnlyWhatIsPastTheMark(t *testing.T) {
	// The buffer holds absolute indices 0, 1 and 2. A caller that has
	// consumed through index 1 must be given index 2 and nothing else.
	got, prog, err := parseRingBuffer(ringOne, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d statements, want only the one past the mark", len(got))
	}
	if got[0].Text != "SELECT 2" {
		t.Errorf("emitted %q, want the last event", got[0].Text)
	}
	if prog.Missed != 0 {
		t.Errorf("Missed is %d, want 0: nothing was lost here", prog.Missed)
	}
}

func TestParseRingBufferCountsWhatPassedThroughUnread(t *testing.T) {
	// 500 processed, 3 held, so indices 0 through 496 are gone. A caller
	// whose mark is 10 missed 487.
	x := `<RingBufferTarget truncated="0" totalEventsProcessed="500" eventCount="3">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>a</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.239Z"><data name="batch_text"><value>b</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.240Z"><data name="batch_text"><value>c</value></data></event>
	</RingBufferTarget>`
	got, prog, err := parseRingBuffer(x, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d statements, want all 3 the buffer still holds", len(got))
	}
	if prog.Missed != 487 {
		t.Errorf("Missed is %d, want 487 (oldest retained is index 497, the mark was 10)", prog.Missed)
	}
}

func TestATruncatedDocumentKeepsTheOldestAndIsStillPlaceable(t *testing.T) {
	// Measured against 2022: driving 4000 events through an unbounded ring
	// buffer gave totalEventsProcessed=4000, eventCount=4000, truncated=1,
	// and 2191 nodes in the document holding markers 0 through 2190. The
	// document keeps the OLDEST of the buffer and drops the newest, so the
	// first node sits at total-eventCount and NOT at total-len(nodes).
	//
	// Getting this backwards is not a near miss. It labels the oldest event
	// with a high index, concludes placement is impossible, emits the whole
	// document every poll, and ships duplicates while discarding what was
	// really missing.
	x := `<RingBufferTarget truncated="1" totalEventsProcessed="500" eventCount="400">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>oldest</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.239Z"><data name="batch_text"><value>next</value></data></event>
	</RingBufferTarget>`
	// The buffer holds indices 100..499. The document holds the first two of
	// those, 100 and 101. A caller at mark 101 has consumed index 100 and
	// must be handed index 101 alone.
	got, prog, err := parseRingBuffer(x, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "next" {
		t.Fatalf("got %d statements %v; placement must survive truncation", len(got), got)
	}
	if !prog.Truncated {
		t.Error("truncation must still be reported, since the tail of the buffer was not returned")
	}
	if prog.Missed != 0 {
		t.Errorf("Missed is %d; nothing was lost before the document here", prog.Missed)
	}
	if prog.Total != 500 {
		t.Errorf("Total is %d, want 500", prog.Total)
	}
}

func TestTruncationCountsTheTailItCouldNotReturn(t *testing.T) {
	// 400 held, 2 returned: 398 events are in the buffer and not in the
	// document. They are not lost forever, but this poll did not see them,
	// and the mark must not advance past them or they never will be.
	x := `<RingBufferTarget truncated="1" totalEventsProcessed="500" eventCount="400">
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.238Z"><data name="batch_text"><value>a</value></data></event>
	  <event name="sql_batch_completed" timestamp="2026-08-30T20:19:50.239Z"><data name="batch_text"><value>b</value></data></event>
	</RingBufferTarget>`
	_, prog, err := parseRingBuffer(x, 100)
	if err != nil {
		t.Fatal(err)
	}
	if prog.Seen != 102 {
		t.Errorf("Seen is %d, want 102: the mark may only advance to the end of what the document actually held", prog.Seen)
	}
}

func TestParseRingBufferOnAnEmptyTarget(t *testing.T) {
	got, prog, err := parseRingBuffer(`<RingBufferTarget truncated="0" totalEventsProcessed="0" eventCount="0"/>`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || prog.Total != 0 || prog.Missed != 0 {
		t.Errorf("an empty target gave %d statements and %+v", len(got), prog)
	}
}
