package agentworkspace

import (
	"context"
	"testing"

	"github.com/devsy-org/devsy/pkg/agent/tunnel"
	"github.com/devsy-org/devsy/pkg/agent/tunnelserver"
	"github.com/devsy-org/devsy/pkg/status"
)

type recordingReporter struct{ events []status.Event }

func (r *recordingReporter) Report(e status.Event) { r.events = append(r.events, e) }

// The nested agent in the bootstrap container reports its phases to the
// bootstrap hop's tunnel server; they must reach the reporter that relays
// them upstream, or the CLI sees no progress until configuring_workspace.
func TestNestedUpServerForwardsStatus(t *testing.T) {
	reporter := &recordingReporter{}
	server := tunnelserver.New(nestedUpServerOptions(runNestedWorkspaceUpParams{
		reporter: reporter,
	})...)

	_, err := server.Status(context.Background(), &tunnel.StatusUpdate{
		Phase: string(status.PhaseCloningRepository),
		State: string(status.StateStarted),
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if len(reporter.events) != 1 {
		t.Fatalf("got %d events, want 1", len(reporter.events))
	}
	got := reporter.events[0]
	if got.Phase != status.PhaseCloningRepository || got.State != status.StateStarted {
		t.Fatalf("got %+v", got)
	}
}
