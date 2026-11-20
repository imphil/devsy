package tunnelserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devsy-org/devsy/pkg/agent/tunnel"
	"github.com/devsy-org/devsy/pkg/credentials"
	"github.com/devsy-org/devsy/pkg/ghcredentials"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestGitHubCredentialsRequiresIndependentPermission(t *testing.T) {
	for _, server := range []*tunnelServer{
		New(),
		New(WithAllowGitCredentials(true), WithAllowDockerCredentials(true)),
		New(WithAllowGitHubCredentials(false)),
	} {
		var lookups atomic.Int32
		server.githubAccounts = func(context.Context) ghcredentials.HostResult {
			lookups.Add(1)
			return ghcredentials.HostResult{}
		}
		response, err := server.GitHubCredentials(context.Background(), &tunnel.Empty{})
		require.ErrorContains(t, err, "forbidden")
		require.Nil(t, response)
		require.Zero(t, lookups.Load())
	}
}

func TestGitHubCredentialsHTTPAndGRPCBoundaryUsesIndependentPermission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			fixture := startGitHubBoundary(t, ctx, allowed)
			ready := fixture.ready
			lookups := fixture.lookups
			require.Zero(t, lookups.Load(), "readiness resolved a token")
			client := credentials.LoopbackHTTPClient()
			defer client.CloseIdleConnections()
			request, err := http.NewRequestWithContext(
				ctx,
				http.MethodPost,
				"http://"+ready.Address+"/github-credentials",
				nil,
			)
			require.NoError(t, err)
			request.Header.Set(credentials.HeaderCredentialsInstance, ready.Instance)
			request.Header.Set(credentials.HeaderCredentialsHostOwner, ready.HostOwner)
			response, err := client.Do(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			var result ghcredentials.HostResult
			require.NoError(t, json.Unmarshal(body, &result))
			want := "rpc_denied"
			calls := int32(0)
			if allowed {
				want = "no_active_accounts"
				calls = 1
			}
			require.Equal(t, want, result.Status)
			require.Equal(t, calls, lookups.Load())
		})
	}
}

type githubBoundary struct {
	ready   credentials.Readiness
	lookups *atomic.Int32
}

func startGitHubBoundary(t *testing.T, ctx context.Context, allowed bool) githubBoundary {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	impl := New(
		WithAllowGitHubCredentials(allowed),
		WithAllowGitCredentials(true),
		WithAllowDockerCredentials(true),
	)
	var lookups atomic.Int32
	impl.githubAccounts = func(context.Context) ghcredentials.HostResult {
		lookups.Add(1)
		return ghcredentials.HostResult{Status: "no_active_accounts"}
	}
	tunnel.RegisterTunnelServer(server, impl)
	go func() { _ = server.Serve(ln) }()
	conn, err := grpc.NewClient(
		ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	httpLn, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	status := credentials.Readiness{
		Version:       credentials.ReadinessVersion,
		Owner:         "vscode",
		HostOwner:     "host",
		Instance:      "instance",
		GitHubAllowed: allowed,
		GitHubState:   "installed",
	}
	done := make(chan error, 1)
	go func() {
		done <- credentials.RunPreparedCredentialsServer(ctx, httpLn, tunnel.NewTunnelClient(conn), status)
	}()
	t.Cleanup(func() {
		_ = httpLn.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("HTTP server failed to stop")
		}
	})
	ready, err := credentials.ProbeReadiness(ctx, httpLn.Addr().(*net.TCPAddr).Port, status)
	require.NoError(t, err)
	return githubBoundary{ready: ready, lookups: &lookups}
}

// upstreamGitHub answers GitHubCredentials like the next host towards the CLI.
type upstreamGitHub struct {
	tunnel.TunnelClient
	calls   atomic.Int32
	message string
	err     error
}

func (u *upstreamGitHub) GitHubCredentials(
	context.Context, *tunnel.Empty, ...grpc.CallOption,
) (*tunnel.Message, error) {
	u.calls.Add(1)
	if u.err != nil {
		return nil, u.err
	}
	return &tunnel.Message{Message: u.message}, nil
}

func TestGitHubCredentialsUpstreamRelaysTheCLIsAnswer(t *testing.T) {
	upstream := &upstreamGitHub{message: `{"accounts":[],"status":"ok"}`}
	// A hop has no permission of its own and never runs gh itself.
	server := New(WithGitHubCredentialsUpstream(upstream))
	server.githubAccounts = func(context.Context) ghcredentials.HostResult {
		t.Fatal("a hop must not look up local accounts")
		return ghcredentials.HostResult{}
	}
	response, err := server.GitHubCredentials(context.Background(), &tunnel.Empty{})
	require.NoError(t, err)
	require.Equal(t, upstream.message, response.GetMessage())
	require.Equal(t, int32(1), upstream.calls.Load())

	denied := &upstreamGitHub{err: errors.New("GitHub CLI credentials forbidden")}
	_, err = New(WithGitHubCredentialsUpstream(denied)).
		GitHubCredentials(context.Background(), &tunnel.Empty{})
	require.ErrorContains(t, err, "forbidden")
}

func TestGitHubCredentialsUpstreamIgnoresNil(t *testing.T) {
	server := New(WithGitHubCredentialsUpstream(nil))
	_, err := server.GitHubCredentials(context.Background(), &tunnel.Empty{})
	require.ErrorContains(t, err, "forbidden")
}
