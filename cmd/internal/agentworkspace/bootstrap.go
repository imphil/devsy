package agentworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
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
	// bootstrapDockerfile builds the image of the disposable container that
	// clones a repository into a content volume before any devcontainer image
	// exists. The nested "agent workspace up" runs in it, including the
	// repository's initializeCommand, so it follows the recipe of the VS Code
	// Dev Containers extension's own volume bootstrap image: the devcontainers
	// Alpine base (bash, git, common tools) plus the same packages, among them
	// a docker CLI able to talk to the daemon dockerConnectionArgs wires in.
	// Scripts written for VS Code's "Clone Repository in Container Volume"
	// therefore find the same environment here.
	bootstrapDockerfile = `FROM mcr.microsoft.com/devcontainers/base:4.0.10-alpine3.23@sha256:8779d14dfb53154e3e5a4909d98f2c4d6be7374f9ec67965502c36331b0bc42d
RUN apk add --no-cache git-lfs nodejs python3 npm make g++ docker-cli docker-cli-buildx docker-cli-compose
`

	// bootstrapImageRepository is the local image name; the tag is derived
	// from bootstrapDockerfile (see bootstrapImage), so a changed recipe gets
	// rebuilt and an unchanged one is reused.
	bootstrapImageRepository = "devsy-volume-bootstrap"

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

	// Volume, bootstrap image and agent come before the nested agent can
	// report anything, and pulling the image can take a while on first use:
	// report this preparation as the start of cloning, so the CLI does not
	// look stuck. The nested agent reports its own phases through the same
	// reporter (see nestedUpServerOptions).
	reporter := tunnelserver.NewTunnelStatusReporter(ctx, tunnelClient)
	var containerID string
	prepareErr := status.Run(ctx, reporter, status.Operation{
		Phase: status.PhaseCloningRepository,
		Step:  bootstrapPrepareStep,
	}, func(ctx context.Context) error {
		id, err := prepareBootstrapContainer(ctx, reporter, dockerCmd, env, volumeName)
		containerID = id
		if err != nil {
			return err
		}
		if err := cmd.injectAgentIntoBootstrapContainer(
			ctx, dockerCmd, env, containerID, workspaceInfo.InjectTimeout,
		); err != nil {
			return fmt.Errorf("inject agent into bootstrap container: %w", err)
		}
		return nil
	})
	if containerID != "" {
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
	}
	if prepareErr != nil {
		return nil, prepareErr
	}

	subpath := provider.ContentVolumeSubpath(
		devcontainer.GetRunnerIDFromWorkspace(workspaceInfo.Workspace),
	)

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
		reporter:     reporter,
		dockerCmd:    dockerCmd,
		dockerEnv:    env,
		containerID:  containerID,
		mountSubpath: subpath,
		debug:        cmd.Debug,
	})
}

// bootstrapPrepareStep names the status step that covers creating the
// content volume and starting the bootstrap container.
const bootstrapPrepareStep = "preparing the source volume"

// bootstrapImage is the local tag of the image built from
// bootstrapDockerfile.
func bootstrapImage() string {
	sum := sha256.Sum256([]byte(bootstrapDockerfile))
	return bootstrapImageRepository + ":" + hex.EncodeToString(sum[:])[:12]
}

// ensureBootstrapImage builds the bootstrap image unless it exists already.
// Like VS Code, it is built locally on first use rather than pulled from a
// registry of ours; this needs the base image registry and the Alpine
// package mirror once.
func ensureBootstrapImage(
	ctx context.Context,
	reporter status.Reporter,
	dockerCmd string,
	env []string,
) error {
	image := bootstrapImage()
	if dockerRun(ctx, dockerCmd, env, []string{"image", "inspect", image}, nil, io.Discard, io.Discard) == nil {
		return nil
	}
	return status.Run(ctx, reporter, status.Operation{
		Phase: status.PhaseCloningRepository,
		Step:  "building the helper image",
	}, func(ctx context.Context) error {
		var output bytes.Buffer
		args := []string{
			"build", "--tag", image,
			"--label", pkgconfig.DockerManagedLabel + "=" + pkgconfig.LabelValueTrue,
			"-",
		}
		if err := dockerRun(
			ctx, dockerCmd, env, args,
			strings.NewReader(bootstrapDockerfile), &output, &output,
		); err != nil {
			return fmt.Errorf("build %s: %s: %w", image, lastLines(output.String(), 20), err)
		}
		return nil
	})
}

// lastLines returns at most n trailing lines of text.
func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// prepareBootstrapContainer creates the content volume and starts the
// bootstrap container on it, returning the container ID.
func prepareBootstrapContainer(
	ctx context.Context,
	reporter status.Reporter,
	dockerCmd string,
	env []string,
	volumeName string,
) (string, error) {
	localDelivery := &delivery.LocalDockerDelivery{DockerCommand: dockerCmd, Environment: env}
	// The content volume is shared by every workspace of the same repository,
	// so it carries no workspace ID: per-workspace label cleanup must never
	// find it. A workspace delete releases its share via
	// delivery.ContentVolumeReleaser instead.
	labels := pkgconfig.DockerVolumeLabels("", pkgconfig.VolumeRoleContent)
	delete(labels, pkgconfig.DockerWorkspaceIDLabel)
	if err := localDelivery.CreateVolume(ctx, volumeName, labels); err != nil {
		return "", fmt.Errorf("create content volume: %w", err)
	}

	if err := ensureBootstrapImage(ctx, reporter, dockerCmd, env); err != nil {
		return "", err
	}

	name := bootstrapContainerName(volumeName)
	// best-effort cleanup of a leftover container from a previous failed attempt
	_ = dockerRun(ctx, dockerCmd, env, []string{"rm", "-f", name}, nil, io.Discard, io.Discard)

	containerID, err := startBootstrapContainer(ctx, dockerCmd, env, name, volumeName)
	if err != nil {
		// It may have started even though its ID could not be read.
		_ = dockerRun(
			context.WithoutCancel(ctx), dockerCmd, env,
			[]string{"rm", "-f", name}, nil, io.Discard, io.Discard,
		)
		return "", fmt.Errorf("start bootstrap container: %w", err)
	}
	return containerID, nil
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
	args = append(args, "--entrypoint", "sleep", bootstrapImage(), "infinity")
	// stderr stays separate: when the image is not present yet, docker run
	// reports the pull there ("Unable to find image ... locally", progress),
	// and only stdout carries the container ID.
	var stderr bytes.Buffer
	if err := dockerRun(ctx, dockerCmd, env, args, nil, &stdout, &stderr); err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()+"\n"+stdout.String()), err)
	}
	return parseContainerID(stdout.String())
}

// parseContainerID returns the container ID that "docker run -d" prints as
// its last line of output.
func parseContainerID(output string) (string, error) {
	lines := strings.Fields(output)
	if len(lines) == 0 {
		return "", fmt.Errorf("docker run printed no container ID")
	}
	id := lines[len(lines)-1]
	if !containerIDPattern.MatchString(id) {
		return "", fmt.Errorf("docker run printed no container ID: %q", strings.TrimSpace(output))
	}
	return id, nil
}

var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

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
