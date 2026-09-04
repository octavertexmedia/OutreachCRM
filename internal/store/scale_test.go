package store

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestBackfillScale guards the batching in backfillPersonEmails. Inserting one
// address at a time took ~43s for a base this size; batched it takes ~3s, and
// this runs after every import. Opt in with TEST_PG_SCALE=1 plus TEST_PG_URL —
// it seeds 65k rows and is too slow for a normal run.
func TestBackfillScale(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" || os.Getenv("TEST_PG_SCALE") == "" {
		t.Skip("set TEST_PG_URL and TEST_PG_SCALE=1 to run the scale guard")
	}
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const n = 65000
	start := time.Now()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO leads (workspace_id, owner_id, name, email, company, title, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, 1, 1,
			fmt.Sprintf("Person %d", i), fmt.Sprintf("p%d@example%d.com", i, i%900),
			fmt.Sprintf("Co %d", i%900), "Manager", fmtTime(now()), fmtTime(now())); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d leads in %s", n, time.Since(start).Round(time.Millisecond))

	start = time.Now()
	res, err := st.BackfillProspects()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("backfill: %+v in %s", res, elapsed.Round(time.Millisecond))
	if res.PeopleCreated != n {
		t.Errorf("PeopleCreated = %d, want %d", res.PeopleCreated, n)
	}
	// Generous ceiling: the point is to catch a regression to per-row inserts,
	// not to police a few hundred milliseconds.
	if elapsed > 20*time.Second {
		t.Errorf("backfill took %s for %d rows — batching has regressed", elapsed.Round(time.Millisecond), n)
	}
}
