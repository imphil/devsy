package agentworkspace

import (
	"context"
	"regexp"
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

func TestParseContainerID(t *testing.T) {
	const id = "4a34e7b78f4471f95fbf3b19306b3b7d04cdec624157ddfde08237e094101b9a"
	for name, tc := range map[string]struct {
		output  string
		want    string
		wantErr bool
	}{
		"plain":        {output: id + "\n", want: id},
		"windows":      {output: id + "\r\n", want: id},
		"empty":        {output: "\n", wantErr: true},
		"pull message": {output: "Unable to find image 'docker:27-cli' locally\n", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseContainerID(tc.output)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBootstrapImage(t *testing.T) {
	image := bootstrapImage()
	if !regexp.MustCompile(`^devsy-volume-bootstrap:[0-9a-f]{12}$`).MatchString(image) {
		t.Fatalf("unexpected image tag %q", image)
	}
	if image != bootstrapImage() {
		t.Fatal("tag must be stable for the same Dockerfile")
	}
	// The base is pinned by digest, like everything else we download.
	if !regexp.MustCompile(`(?m)^FROM \S+@sha256:[0-9a-f]{64}$`).MatchString(bootstrapDockerfile) {
		t.Fatalf("base image not pinned by digest:\n%s", bootstrapDockerfile)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("got %q", got)
	}
	if got := lastLines("a", 5); got != "a" {
		t.Fatalf("got %q", got)
	}
}
