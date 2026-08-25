package service

import "testing"

func TestJournalReportsTruncationRatherThanASilentHole(t *testing.T) {
	journal := NewJournal(4)
	for range 6 {
		journal.Record(Transition{Kind: TransitionServiceState, Service: "api", To: "running"})
	}
	// A reader that asked from a point the bounded journal has already
	// dropped must be told its story has a hole, not handed what survived.
	transitions, oldest, latest, truncated := journal.Since(1, 0)
	if oldest != 3 || latest != 6 || !truncated {
		t.Fatalf("bounds = %d..%d truncated=%v", oldest, latest, truncated)
	}
	if len(transitions) != 4 {
		t.Fatalf("transitions = %d", len(transitions))
	}
	if transitions[0].Sequence != 3 || transitions[3].Sequence != 6 {
		t.Fatalf("sequences = %d..%d", transitions[0].Sequence, transitions[3].Sequence)
	}
	if current, _, _, stillTruncated := journal.Since(6, 0); len(current) != 0 || stillTruncated {
		t.Fatalf("caught-up read = %#v truncated=%v", current, stillTruncated)
	}
	if journal.Latest() != 6 {
		t.Fatalf("latest = %d", journal.Latest())
	}
}

func TestNilJournalRecordsNothingWithoutPanicking(t *testing.T) {
	var journal *Journal
	if sequence := journal.Record(Transition{To: "running"}); sequence != 0 {
		t.Fatalf("sequence = %d", sequence)
	}
	if transitions, _, _, _ := journal.Since(0, 0); transitions != nil {
		t.Fatalf("transitions = %#v", transitions)
	}
}
