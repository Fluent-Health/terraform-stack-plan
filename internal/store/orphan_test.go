package store

import "testing"

func TestOpenGrantPRsCollapsesMultiEnv(t *testing.T) {
	// A PR with open grants in two environments must appear as ONE row (GROUP BY
	// pr) — the sweep checks the PR once and revokeOrphans handles every env.
	db := newTestDB(t)
	if _, err := db.Exec(
		`INSERT INTO executions (id, repo, sha, pr, environment) VALUES (?,?,?,?,?)`,
		"e1", "o/r", "sha", 7, "staging"); err != nil {
		t.Fatal(err)
	}
	seedGateTargetSQL(t, db, 7, "staging", "iam", "p1", "g1", "ACTIVE")
	seedGateTargetSQL(t, db, 7, "prod", "iam", "p1", "g2", "AWAITING")
	got, err := OpenGrantPRs(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PR != 7 {
		t.Fatalf("multi-env PR must collapse to one row, got %+v", got)
	}
}

func TestOpenGrantPRs(t *testing.T) {
	db := newTestDB(t)
	mkExec := func(id, repo string, pr int, env string) {
		if _, err := db.Exec(
			`INSERT INTO executions (id, repo, sha, pr, environment) VALUES (?,?,?,?,?)`,
			id, repo, "sha", pr, env); err != nil {
			t.Fatal(err)
		}
	}
	// PR 7: fully-ACTIVE (Satisfied) gate — the merged-PR orphan PendingGates omits.
	mkExec("e7", "o/r7", 7, "staging")
	seedGateTargetSQL(t, db, 7, "staging", "iam", "p1", "g1", "ACTIVE")
	// PR 8: only a terminal REVOKED grant — no open grant, must be excluded.
	mkExec("e8", "o/r8", 8, "staging")
	seedGateTargetSQL(t, db, 8, "staging", "iam", "p1", "g2", "REVOKED")
	// PR 9: AWAITING (open) — included, with its repo.
	mkExec("e9", "o/r9", 9, "prod")
	seedGateTargetSQL(t, db, 9, "prod", "iam", "p2", "g3", "AWAITING")

	got, err := OpenGrantPRs(db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{7: "o/r7", 9: "o/r9"} // PR 8 excluded (terminal only)
	if len(got) != len(want) {
		t.Fatalf("OpenGrantPRs = %+v, want PRs %v", got, want)
	}
	for _, p := range got {
		repo, ok := want[p.PR]
		if !ok {
			t.Fatalf("unexpected PR %d in result", p.PR)
		}
		if p.Repo != repo {
			t.Fatalf("PR %d repo = %q, want %q", p.PR, p.Repo, repo)
		}
	}
}
