package agentworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/devsy-org/devsy/pkg/agent"
	"github.com/devsy-org/devsy/pkg/agent/delivery"
	"github.com/devsy-org/devsy/pkg/agent/tunnel"
	"github.com/devsy-org/devsy/pkg/agent/tunnelserver"
	"github.com/devsy-org/devsy/pkg/compress"
	pkgconfig "github.com/devsy-org/devsy/pkg/config"
	"github.com/devsy-org/devsy/pkg/devcontainer"
	config2 "github.com/devsy-org/devsy/pkg/devcontainer/config"
	"github.com/devsy-org/devsy/pkg/devcontainer/sshtunnel"
	"github.com/devsy-org/devsy/pkg/log"
	"github.com/devsy-org/devsy/pkg/provider"
	"github.com/devsy-org/devsy/pkg/status"
)

const (
	// bootstrapImage is used for the disposable container that clones a
	// repository into a content volume before any devcontainer image exists.
	// It needs a shell (for agent injection) and, crucially, a docker CLI
	// able to talk to the daemon connection dockerConnectionArgs wires in:
	// the nested "agent workspace up" running inside it installs git itself
	// if it's missing, but pkg/dockerinstall's auto-install only supports
	// apt/yum-based distros, not Alpine's apk -- so a plain "alpine" base
	// would leave the nested process unable to acquire a docker CLI at all.
	// The official docker CLI image is Alpine-based (apk is still available
	// for git) and already ships the docker binary, sidestepping that gap.
	bootstrapImage = "docker:27-cli"

	// bootstrapMountTarget is where the content volume is mounted inside the
	// bootstrap container, and becomes the nested invocation's ContentFolder
	// (at its worktrees/<uid> subpath).
	bootstrapMountTarget = "/workspace-content"

	bootstrapContainerUser = "root"
)

// runInBootstrapContainer clones a --clone-into-volume workspace's git
// source into its content volume and brings up its real devcontainer
// against that volume. It does this by starting a disposable "bootstrap"
// container that has both the content volume and this process's own docker
// daemon connection mounted in (see startBootstrapContainer/
// dockerConnectionArgs), injecting the devsy agent into it, and running a
// full, unmodified "internal agent workspace up" inside it over the same
// SSH-over-stdio tunnel pattern already used to reach "the machine" from the
// CLI (cmd/workspace/up/agent.go's devsyUpMachineSSH) -- this is one more
// hop of that same pattern, reusing tunnelserver.RunUpServer unchanged for
// credentials and the final result.
//
// Because the nested process sees the content volume as an ordinary local
// directory and reaches the exact same docker daemon through the mounted
// connection, every existing devcontainer.json parsing, image-build and
// container-run code path runs completely unmodified against it. No host-
// disk copy of the repository is ever needed, for parsing or for building.
// The nested invocation carries ContentVolumeMounted=true, so
// pkg/devcontainer/mount.go's getWorkspace mounts the real container's
// workspace folder from the same named volume instead of a host bind path,
// so the content outlives the bootstrap container.
func (cmd *UpCmd) runInBootstrapContainer(
	ctx context.Context,
	workspaceInfo *provider.AgentWorkspaceInfo,
	tunnelClient tunnel.TunnelClient,
) (*config2.Result, error) {
	if workspaceInfo.Agent.Driver != provider.DockerDriver &&
		workspaceInfo.Agent.Driver != "" {
		return nil, fmt.Errorf(
			"clone-into-volume is only supported with the docker provider, got driver %q",
			workspaceInfo.Agent.Driver,
		)
	}

	env := dockerEnvSlice(workspaceInfo.Agent.Docker.Env)
	dockerCmd := "docker"
	if workspaceInfo.Agent.Docker.Path != "" {
		dockerCmd = workspaceInfo.Agent.Docker.Path
	}

	volumeName := workspaceInfo.Workspace.ContentVolumeName
	if volumeName == "" {
		volumeName = provider.ContentVolumeName(workspaceInfo.Workspace.Source.GitRepository)
	}

	localDelivery := &delivery.LocalDockerDelivery{DockerCommand: dockerCmd, Environment: env}
	// The content volume is shared by every workspace of the same repository,
	// so it carries no workspace ID: per-workspace label cleanup must never
	// find it. A workspace delete releases its share via
	// delivery.ContentVolumeReleaser instead.
	labels := pkgconfig.DockerVolumeLabels("", pkgconfig.VolumeRoleContent)
	delete(labels, pkgconfig.DockerWorkspaceIDLabel)
	if err := localDelivery.CreateVolume(ctx, volumeName, labels); err != nil {
		return nil, fmt.Errorf("create content volume: %w", err)
	}

	name := bootstrapContainerName(volumeName)
	// best-effort cleanup of a leftover container from a previous failed attempt
	_ = dockerRun(ctx, dockerCmd, env, []string{"rm", "-f", name}, nil, io.Discard, io.Discard)

	containerID, err := startBootstrapContainer(ctx, dockerCmd, env, name, volumeName)
	if err != nil {
		return nil, fmt.Errorf("start bootstrap container: %w", err)
	}
	defer func() {
		// use a fresh context: ctx may already be canceled/expired by the time
		// we get here, but the bootstrap container still needs to be torn down.
		var stderr bytes.Buffer
		if stopErr := dockerRun(
			context.WithoutCancel(ctx), dockerCmd, env,
			[]string{"rm", "-f", containerID}, nil, io.Discard, &stderr,
		); stopErr != nil {
			log.Errorf("stop bootstrap container: %s: %v", stderr.String(), stopErr)
		}
	}()

	subpath := provider.ContentVolumeSubpath(
		devcontainer.GetRunnerIDFromWorkspace(workspaceInfo.Workspace),
	)
	if err := cmd.injectAgentIntoBootstrapContainer(
		ctx, dockerCmd, env, containerID, workspaceInfo.InjectTimeout,
	); err != nil {
		return nil, fmt.Errorf("inject agent into bootstrap container: %w", err)
	}

	// Secrets no longer travel inside the workspace info: the nested agent
	// fetches them over its tunnel, so relay them from our own upstream.
	secretResponse, err := tunnelClient.Secrets(ctx, &tunnel.Empty{})
	if err != nil {
		return nil, fmt.Errorf("fetch workspace secrets: %w", err)
	}
	secretsEnv, secretsMount := splitSecrets(secretResponse.GetSecrets())

	return runNestedWorkspaceUp(ctx, runNestedWorkspaceUpParams{
		workspaceInfo: workspaceInfo,
		secretsEnv:    secretsEnv,
		secretsMount:  secretsMount,
		// The nested agent reports its phases (cloning, building, starting
		// the container, ...) to us; relay them upstream like our own.
		reporter:     tunnelserver.NewTunnelStatusReporter(ctx, tunnelClient),
		dockerCmd:    dockerCmd,
		dockerEnv:    env,
		containerID:  containerID,
		mountSubpath: subpath,
		debug:        cmd.Debug,
	})
}

func bootstrapContainerName(volumeName string) string {
	return volumeName + "-bootstrap"
}

func startBootstrapContainer(
	ctx context.Context,
	dockerCmd string,
	env []string,
	name, volumeName string,
) (string, error) {
	var stdout bytes.Buffer
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--mount", fmt.Sprintf(
			"type=volume,source=%s,target=%s", volumeName, bootstrapMountTarget,
		),
	}
	args = append(args, dockerConnectionArgs(env)...)
	args = append(args, "--entrypoint", "sleep", bootstrapImage, "infinity")
	if err := dockerRun(ctx, dockerCmd, env, args, nil, &stdout, &stdout); err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(stdout.String()), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// dockerConnectionArgs returns the docker run flags that give the bootstrap
// container access to the same docker daemon this process itself talks to,
// so the nested "internal agent workspace up" running inside it can build
// and start the real devcontainer on that same daemon instead of a nested
// Docker-in-Docker. When the resolved docker environment points at a
// remote/TCP daemon, the connection details are passed through as
// environment (and, for TLS setups, a bind-mounted cert directory);
// otherwise the local docker socket is bind-mounted, which covers Docker
// Desktop, plain Linux dockerd, Colima and similar local setups.
func dockerConnectionArgs(env []string) []string {
	host := lookupEnv(env, "DOCKER_HOST")
	if host == "" {
		return []string{
			"--mount", "type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock",
		}
	}

	args := []string{"-e", "DOCKER_HOST=" + host}
	if certPath := lookupEnv(env, "DOCKER_CERT_PATH"); certPath != "" {
		args = append(
			args,
			"-e", "DOCKER_CERT_PATH="+certPath,
			"--mount", fmt.Sprintf("type=bind,source=%s,target=%s", certPath, certPath),
		)
	}
	if tlsVerify := lookupEnv(env, "DOCKER_TLS_VERIFY"); tlsVerify != "" {
		args = append(args, "-e", "DOCKER_TLS_VERIFY="+tlsVerify)
	}
	return args
}

func lookupEnv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v
		}
	}
	return os.Getenv(key)
}

func dockerEnvSlice(env map[string]string) []string {
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// dockerRun shells out to the docker CLI directly (not through
// pkg/agent/delivery, which only exposes volume-lifecycle helpers) for the
// bootstrap container's own lifecycle: start, teardown, and (via
// dockerExec) command execution inside it.
func dockerRun(
	ctx context.Context,
	dockerCmd string,
	env []string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	// #nosec G204 -- args are constructed internally, not from user input
	c := exec.CommandContext(ctx, dockerCmd, args...)
	if env != nil {
		c.Env = append(os.Environ(), env...)
	}
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Run()
}

func dockerExec(
	ctx context.Context,
	dockerCmd string,
	env []string,
	containerID, command string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	args := []string{"exec"}
	if stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, "-u", bootstrapContainerUser, containerID, "sh", "-c", command)
	return dockerRun(ctx, dockerCmd, env, args, stdin, stdout, stderr)
}

func (cmd *UpCmd) injectAgentIntoBootstrapContainer(
	ctx context.Context,
	dockerCmd string,
	env []string,
	containerID string,
	timeout time.Duration,
) error {
	err := agent.InjectAgent(ctx, &agent.InjectOptions{
		Exec: func(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) error {
			return dockerExec(ctx, dockerCmd, env, containerID, command, stdin, stdout, stderr)
		},
		IsLocal:                     false,
		RemoteAgentPath:             pkgconfig.ContainerDevsyHelperLocation,
		DownloadURL:                 pkgconfig.DefaultAgentDownloadURL(),
		PreferDownloadFromRemoteUrl: new(false),
		Timeout:                     timeout,
	})
	if err != nil {
		return fmt.Errorf("inject agent: %w", err)
	}
	return nil
}

type runNestedWorkspaceUpParams struct {
	workspaceInfo *provider.AgentWorkspaceInfo
	secretsEnv    []string
	secretsMount  []string
	dockerCmd     string
	dockerEnv     []string
	containerID   string
	mountSubpath  string
	debug         bool
	reporter      status.Reporter
}

// runNestedWorkspaceUp runs "internal agent workspace up" inside
// containerID (the bootstrap container), over the same kind of SSH-over-
// stdio tunnel and agent-injection machinery used everywhere else in this
// codebase (e.g. cmd/workspace/up/agent.go's devsyUpMachineSSH) -- this is
// just one more hop of the same pattern. Credentials are served fresh from
// this process's own environment via tunnelserver.RunUpServer, exactly like
// every other tier boundary in devsy already works.
func runNestedWorkspaceUp(
	ctx context.Context,
	p runNestedWorkspaceUpParams,
) (*config2.Result, error) {
	nested := provider.CloneAgentWorkspaceInfo(p.workspaceInfo)
	nested.ContentFolder = bootstrapMountTarget + "/" + p.mountSubpath
	nested.ContentVolumeMounted = true
	nested.ContentVolumeSubpath = p.mountSubpath

	marshalled, err := json.Marshal(nested)
	if err != nil {
		return nil, fmt.Errorf("marshal nested workspace info: %w", err)
	}
	compressed, err := compress.Compress(string(marshalled))
	if err != nil {
		return nil, fmt.Errorf("compress nested workspace info: %w", err)
	}

	upCommand := buildNestedUpCommand(compressed, p.debug)
	sshTunnelCmd := buildSSHTunnelCommand(p.debug)

	agentInjectFunc := func(
		cancelCtx context.Context,
		sshCmd string,
		sshTunnelStdinReader io.Reader, sshTunnelStdoutWriter io.Writer,
		writer io.WriteCloser,
	) error {
		return dockerExec(
			cancelCtx, p.dockerCmd, p.dockerEnv, p.containerID, sshCmd,
			sshTunnelStdinReader, sshTunnelStdoutWriter, writer,
		)
	}

	return sshtunnel.ExecuteCommand(ctx, sshtunnel.ExecuteCommandOptions{
		Client:         nil,
		AddPrivateKeys: false,
		AgentInject:    agentInjectFunc,
		SSHCommand:     sshTunnelCmd,
		Command:        upCommand,
		TunnelServerFunc: func(
			ctx context.Context,
			stdin io.WriteCloser,
			stdout io.Reader,
		) (*config2.Result, error) {
			return tunnelserver.RunUpServer(ctx, tunnelserver.UpSpec{
				Reader:                 stdout,
				Writer:                 stdin,
				AllowGitCredentials:    p.workspaceInfo.Agent.InjectGitCredentials == pkgconfig.BoolTrue,
				AllowDockerCredentials: p.workspaceInfo.Agent.InjectDockerCredentials == pkgconfig.BoolTrue,
				Workspace:              p.workspaceInfo.Workspace,
				Options:                nestedUpServerOptions(p),
			})
		},
	})
}

// nestedUpServerOptions configures the tunnel server the nested agent talks
// to: it serves the relayed secrets and forwards the nested agent's status
// events upstream, so the CLI sees the same phases as without a bootstrap
// container.
func nestedUpServerOptions(p runNestedWorkspaceUpParams) []tunnelserver.Option {
	return []tunnelserver.Option{
		tunnelserver.WithSecrets(p.secretsEnv, p.secretsMount),
		tunnelserver.WithStatusReporter(p.reporter),
	}
}

func buildSSHTunnelCommand(debug bool) string {
	cmd := fmt.Sprintf("'%s' internal ssh-server --stdio", pkgconfig.ContainerDevsyHelperLocation)
	if debug {
		cmd += " --debug"
	}
	return cmd
}

func buildNestedUpCommand(workspaceInfoCompressed string, debug bool) string {
	cmd := fmt.Sprintf(
		"%q internal agent workspace up --workspace-info %q",
		pkgconfig.ContainerDevsyHelperLocation,
		workspaceInfoCompressed,
	)
	if debug {
		cmd += " --debug"
	}
	return cmd
}
