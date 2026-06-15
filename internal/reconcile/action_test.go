package reconcile

import "testing"

func TestActionVariantsImplementInterface(t *testing.T) {
	var as []Action = []Action{
		RequestGrant{Class: "iam", Target: "p"},
		RevokeGrant{Class: "iam", Target: "p", PR: 7, Environment: "staging"},
		RenderCheckRun{Terminal: true, Conclusion: "success"},
		PostCommitStatus{State: "success"},
		PublishSSE{},
	}
	if len(as) != 5 {
		t.Fatalf("want 5 actions, got %d", len(as))
	}
}
