package helps

import "testing"

func TestClaudeDiagnosticsTracksCompletedMessagePerCredentialSession(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, sequence, previous := BeginClaudeDiagnostics("credential-a", "session-a")
	if key == "" || sequence != 1 || previous != "" {
		t.Fatalf("first begin = %q/%d/%q, want key/1/empty", key, sequence, previous)
	}
	CommitClaudeDiagnostics(key, sequence, "msg_first")
	_, secondSequence, previous := BeginClaudeDiagnostics("credential-a", "session-a")
	if secondSequence != 2 || previous != "msg_first" {
		t.Fatalf("second begin = %d/%q, want 2/msg_first", secondSequence, previous)
	}

	_, _, otherSession := BeginClaudeDiagnostics("credential-a", "session-b")
	_, _, otherCredential := BeginClaudeDiagnostics("credential-b", "session-a")
	if otherSession != "" || otherCredential != "" {
		t.Fatalf("diagnostics leaked across identity: session=%q credential=%q", otherSession, otherCredential)
	}
}

func TestClaudeDiagnosticsRejectsLateOlderCommit(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, first, _ := BeginClaudeDiagnostics("credential", "session")
	_, second, _ := BeginClaudeDiagnostics("credential", "session")
	CommitClaudeDiagnostics(key, second, "msg_newer")
	CommitClaudeDiagnostics(key, first, "msg_older")
	_, _, previous := BeginClaudeDiagnostics("credential", "session")
	if previous != "msg_newer" {
		t.Fatalf("previous message = %q, want newer completed generation", previous)
	}
}
