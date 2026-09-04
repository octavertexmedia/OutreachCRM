package store

import "testing"

func TestCursorStartsAtZero(t *testing.T) {
	st := newTestStore(t)
	c, err := st.GetCursor(ImportSourceSmartlead, ResourceStatistics, 42)
	if err != nil {
		t.Fatal(err)
	}
	if c.NextOffset != 0 || c.Completed {
		t.Errorf("fresh cursor = %+v, want offset 0 and not completed", c)
	}
}

func TestCursorResumesFromLastCommittedPage(t *testing.T) {
	st := newTestStore(t)

	if err := st.SaveCursorProgress(ImportSourceSmartlead, ResourceStatistics, 42, 1000, 1000, 45415); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCursorProgress(ImportSourceSmartlead, ResourceStatistics, 42, 2000, 2000, 45415); err != nil {
		t.Fatal(err)
	}

	c, err := st.GetCursor(ImportSourceSmartlead, ResourceStatistics, 42)
	if err != nil {
		t.Fatal(err)
	}
	if c.NextOffset != 2000 {
		t.Errorf("NextOffset = %d, want 2000 — a restart must not redo committed pages", c.NextOffset)
	}
	if c.RowsDone != 2000 || c.TotalHint != 45415 {
		t.Errorf("RowsDone/TotalHint = %d/%d, want 2000/45415", c.RowsDone, c.TotalHint)
	}
	if c.Completed {
		t.Error("cursor should not be complete yet")
	}
}

func TestCursorFailureKeepsOffset(t *testing.T) {
	st := newTestStore(t)
	if err := st.SaveCursorProgress(ImportSourceSmartlead, ResourceLeads, 7, 3000, 3000, 9000); err != nil {
		t.Fatal(err)
	}
	if err := st.FailCursor(ImportSourceSmartlead, ResourceLeads, 7, "connection reset by peer"); err != nil {
		t.Fatal(err)
	}

	c, err := st.GetCursor(ImportSourceSmartlead, ResourceLeads, 7)
	if err != nil {
		t.Fatal(err)
	}
	// The whole point: a failure records why without throwing away progress.
	if c.NextOffset != 3000 {
		t.Errorf("NextOffset = %d, want 3000 preserved across a failure", c.NextOffset)
	}
	if c.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", c.Attempts)
	}
	if c.LastError == "" {
		t.Error("LastError should record the cause")
	}

	// A successful page after a failure clears the error.
	if err := st.SaveCursorProgress(ImportSourceSmartlead, ResourceLeads, 7, 4000, 4000, 9000); err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetCursor(ImportSourceSmartlead, ResourceLeads, 7)
	if c.LastError != "" {
		t.Errorf("LastError = %q, want cleared after a good page", c.LastError)
	}
}

func TestCursorCompletionAndOpenList(t *testing.T) {
	st := newTestStore(t)
	if err := st.SaveCursorProgress(ImportSourceSmartlead, ResourceStatistics, 1, 500, 500, 500); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCursorProgress(ImportSourceSmartlead, ResourceStatistics, 2, 100, 100, 900); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteCursor(ImportSourceSmartlead, ResourceStatistics, 1); err != nil {
		t.Fatal(err)
	}

	open, err := st.ListOpenCursors(ImportSourceSmartlead)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("ListOpenCursors = %d, want 1 (campaign 1 finished)", len(open))
	}
	if open[0].CampaignID != 2 {
		t.Errorf("open cursor is campaign %d, want 2", open[0].CampaignID)
	}

	done, err := st.GetCursor(ImportSourceSmartlead, ResourceStatistics, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !done.Completed || done.CompletedAt == nil {
		t.Error("campaign 1 should be marked complete")
	}
}

func TestResetCursor(t *testing.T) {
	st := newTestStore(t)
	if err := st.SaveCursorProgress(ImportSourceSmartlead, ResourceHistory, 3, 900, 900, 900); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetCursor(ImportSourceSmartlead, ResourceHistory, 3); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetCursor(ImportSourceSmartlead, ResourceHistory, 3)
	if err != nil {
		t.Fatal(err)
	}
	if c.NextOffset != 0 {
		t.Errorf("NextOffset = %d after reset, want 0", c.NextOffset)
	}
}
