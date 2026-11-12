// Package compose starts and stops the network's containers.
//
// It runs Docker Compose in process: compose-go loads the compose files and
// the Docker Compose library applies them through the Docker API, as
// `docker compose up` and `down` do. The image no longer needs the docker
// and docker-compose binaries, and the containers carry the usual compose
// labels, so `docker compose -p <project> ...` still works on them.
package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	composesvc "github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// NetworkKey is the compose network the nodes share.
const NetworkKey = "byfn"

// Project is the network's compose project.
type Project struct {
	dockerCli command.Cli
	service   api.Compose
	project   *types.Project
	out       io.Writer
}

// Options say which compose project to load.
type Options struct {
	// Env is added to the process environment and the .env file for
	// interpolation, and wins over both, as the variables byfn.sh set on
	// the docker-compose command line did.
	Env map[string]string
	// Out receives the progress of compose.
	Out io.Writer
	// Dir is the project directory; Files are relative to it.
	Dir  string
	Name string
	// PublishAddr is the host address that a published port binds to when
	// the compose file gives none. Empty leaves the ports as the files say.
	PublishAddr string
	Files       []string
	// ForwardEnv names the services (the chaincode containers) that get
	// each variable of the process environment whose name starts with one
	// of ForwardEnvPrefixes. A compose file can pass a variable only by
	// name, so docker-compose-ccaas.yaml passes a fixed list
	// (CHAINCODE_OTLP_TRACER_ENDPOINT, CHAINCODE_LOG_LEVEL) and the
	// chaincode never saw the others it reads, such as OTEL_BSP_* for its
	// span batches. A variable the compose file sets keeps that value.
	ForwardEnv []string
}

// ForwardEnvPrefixes are the prefixes of the variables that Load passes to
// the ForwardEnv services: the chaincode's own settings (CHAINCODE_,
// SUBSTRATE_) and the OpenTelemetry SDK's (OTEL_).
var ForwardEnvPrefixes = []string{"CHAINCODE_", "SUBSTRATE_", "OTEL_"}

// Load loads the compose project that o names.
func Load(ctx context.Context, o Options) (*Project, error) {
	dir, name, files, env, out := o.Dir, o.Name, o.Files, o.Env, o.Out
	dockerCli, err := command.NewDockerCli(command.WithOutputStream(out), command.WithErrorStream(out))
	if err != nil {
		return nil, err
	}
	if err = dockerCli.Initialize(flags.NewClientOptions()); err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, filepath.Join(dir, f))
	}
	var envList []string
	for _, k := range slices.Sorted(maps.Keys(env)) {
		envList = append(envList, k+"="+env[k])
	}
	opts, err := cli.NewProjectOptions(paths,
		cli.WithWorkingDirectory(dir),
		cli.WithOsEnv,
		// With no argument, WithEnvFiles names the project's .env;
		// WithDotEnv reads only the files named before it.
		cli.WithEnvFiles(),
		cli.WithDotEnv,
		cli.WithEnv(envList),
		cli.WithName(name),
		cli.WithResolvedPaths(true),
	)
	if err != nil {
		return nil, err
	}
	// WithDotEnv does not override a variable already set, so set ours
	// again after it.
	maps.Copy(opts.Environment, env)
	project, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, err
	}
	forwardEnv(project, o.ForwardEnv, os.Environ(), env)
	publishOn(project, o.PublishAddr)
	// The labels the compose CLI sets: compose finds a project's containers
	// by them.
	for name, s := range project.Services {
		s.CustomLabels = map[string]string{
			api.ProjectLabel:     project.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  project.WorkingDir,
			api.ConfigFilesLabel: strings.Join(project.ComposeFiles, ","),
			api.OneoffLabel:      "False",
		}
		project.Services[name] = s
	}
	// Answer yes to every prompt, as `docker compose up -y` does: the
	// builder runs without a terminal, and a prompt must not block it.
	service, err := composesvc.NewComposeService(dockerCli, composesvc.WithPrompt(func(string, bool) (bool, error) { return true, nil }))
	if err != nil {
		return nil, err
	}
	return &Project{
		dockerCli: dockerCli,
		service:   service,
		project:   project,
		out:       out,
	}, nil
}

// Up creates and starts the containers, as `docker compose up -d`.
func (p *Project) Up(ctx context.Context) error {
	return p.service.Up(ctx, p.project, api.UpOptions{
		Create: api.CreateOptions{
			Recreate:             api.RecreateDiverged,
			RecreateDependencies: api.RecreateDiverged,
		},
		Start: api.StartOptions{Project: p.project},
	})
}

// Down stops and removes the containers, networks and volumes, as
// `docker compose down --volumes`.
func (p *Project) Down(ctx context.Context) error {
	return p.service.Down(ctx, p.project.Name, api.DownOptions{Project: p.project, Volumes: true, RemoveOrphans: true})
}

func (p *Project) client() client.APIClient { return p.dockerCli.Client() }

// NetworkName is the Docker name of the nodes' network.
func (p *Project) NetworkName() (string, error) {
	n, ok := p.project.Networks[NetworkKey]
	if !ok {
		return "", fmt.Errorf("the compose files define no %q network", NetworkKey)
	}
	return n.Name, nil
}

// runningNetwork is the name of the network the running nodes are on. It
// can differ from NetworkName: the CCaaS compose file names the network
// "byfn", and without that file compose names it <project>_byfn. A network
// started before generatecc wrote the CCaaS file has the second name, and
// install, which loads that file, must still reach it.
func (p *Project) runningNetwork(ctx context.Context) (string, error) {
	want, err := p.NetworkName()
	if err != nil {
		return "", err
	}
	names := slices.Sorted(maps.Keys(p.project.Services))
	for _, s := range names {
		cname := p.project.Services[s].ContainerName
		if cname == "" {
			continue
		}
		r, ierr := p.client().ContainerInspect(ctx, cname, client.ContainerInspectOptions{})
		c := r.Container
		if ierr != nil || c.NetworkSettings == nil {
			continue
		}
		if _, ok := c.NetworkSettings.Networks[want]; ok {
			return want, nil
		}
		for n := range c.NetworkSettings.Networks {
			if isNetworkFor(n, p.project.Name) {
				return n, nil
			}
		}
	}
	return want, nil
}

// isNetworkFor reports whether a network name is one this project's byfn
// network can have: "byfn" or "<project>_byfn".
func isNetworkFor(name, project string) bool {
	return name == NetworkKey || name == project+"_"+NetworkKey
}

// selfContainer returns the ID of the container the builder runs in, or ""
// when it runs on a host. Consumers run the builder in a container with the
// Docker socket mounted; Docker sets the container's host name to its ID.
func (p *Project) selfContainer(ctx context.Context) (string, error) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return "", nil //nolint:nilerr // not in a Docker container
	}
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	r, err := p.client().ContainerInspect(ctx, host, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		// A container with another host name, or another daemon.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.Container.ID, nil
}

// A Route takes the builder to the nodes of the network.
type Route struct {
	// Resolve gives the address to dial for a node's host name and port.
	Resolve func(host string, port int) string
	leave   func()
}

// Close leaves the network, if Route joined it, so `down` can remove it.
func (r *Route) Close() { r.leave() }

// Route gives the builder a route to the nodes. The nodes' admin ports
// (the orderers' 7053) are not published, so the builder dials each node's
// IP on the network. In a container, the builder first joins the network,
// until Close.
func (p *Project) Route(ctx context.Context) (*Route, error) {
	netName, err := p.runningNetwork(ctx)
	if err != nil {
		return nil, err
	}
	leave := func() {}
	self, err := p.selfContainer(ctx)
	if err != nil {
		return nil, err
	}
	if self != "" {
		r, ierr := p.client().ContainerInspect(ctx, self, client.ContainerInspectOptions{})
		if ierr != nil {
			return nil, ierr
		}
		if _, ok := r.Container.NetworkSettings.Networks[netName]; !ok {
			if _, err = p.client().NetworkConnect(ctx, netName, client.NetworkConnectOptions{Container: self, EndpointConfig: &network.EndpointSettings{}}); err != nil {
				return nil, fmt.Errorf("join network %s: %w", netName, err)
			}
			leave = func() {
				// Use a fresh context: the caller's may be done.
				_, _ = p.client().NetworkDisconnect(context.Background(), netName, client.NetworkDisconnectOptions{Container: self, Force: true})
			}
		}
	}
	var mu sync.Mutex
	ips := map[string]string{}
	resolve := func(host string, port int) string {
		mu.Lock()
		defer mu.Unlock()
		ip, ok := ips[host]
		if !ok {
			// The node containers are named after their host names
			// (container_name). Fall back to the name, which the Docker DNS
			// resolves inside the network.
			// Cache only a found address, so a retry after a failed lookup
			// (a container not yet on the network) looks again.
			ip = host
			if r, err := p.client().ContainerInspect(ctx, host, client.ContainerInspectOptions{}); err == nil && r.Container.NetworkSettings != nil {
				if ep, ok := r.Container.NetworkSettings.Networks[netName]; ok && ep.IPAddress.IsValid() {
					ip = ep.IPAddress.String()
					ips[host] = ip
				}
			}
		}
		return fmt.Sprintf("%s:%d", ip, port)
	}
	return &Route{Resolve: resolve, leave: leave}, nil
}

// devPeer matches the containers and images the peers create to run Go
// chaincode: dev-peer0.org1.example.com-<label>-<hash>.
var devPeer = regexp.MustCompile(`^/?dev-peer\d+\.`)

// RemoveChaincodeContainers removes the containers and images the peers
// built for Go chaincode. byfn.sh removed the same set; the Python builder
// also removed every image whose listing matched "dev", "none", "test-vp"
// or "peer<n>-", which could remove images of other projects, so that is
// gone.
func (p *Project) RemoveChaincodeContainers(ctx context.Context) error {
	cs, err := p.client().ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("name", "dev-peer")})
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cs.Items {
		if !slices.ContainsFunc(c.Names, devPeer.MatchString) {
			continue
		}
		_, _ = fmt.Fprintf(p.out, "removing container %s\n", strings.TrimPrefix(c.Names[0], "/"))
		_, rerr := p.client().ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		errs = append(errs, rerr)
	}
	imgs, err := p.client().ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, img := range imgs.Items {
		if !slices.ContainsFunc(img.RepoTags, devPeer.MatchString) {
			continue
		}
		_, _ = fmt.Fprintf(p.out, "removing image %s\n", img.RepoTags[0])
		_, err := p.client().ImageRemove(ctx, img.ID, client.ImageRemoveOptions{Force: true})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Close releases the Docker client.
func (p *Project) Close() error { return p.client().Close() }

// forwardEnv adds to each named service the variables of environ whose names
// start with one of ForwardEnvPrefixes, unless the service sets them. A name
// the file lists without a value ("CHAINCODE_LOG_LEVEL:") is resolved from
// the environment, or nil when unset, so a nil one is filled too. The
// builder's own interpolation variables (skip: CHAINCODE_VERSION, CCID_*)
// are not forwarded: a CHAINCODE_VERSION left in the shell must not
// disagree with the --cc-version the image tag came from.
func forwardEnv(project *types.Project, services []string, environ []string, skip map[string]string) {
	for _, name := range services {
		s, ok := project.Services[name]
		if !ok {
			continue
		}
		if s.Environment == nil {
			s.Environment = types.MappingWithEquals{}
		}
		for _, kv := range environ {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || !slices.ContainsFunc(ForwardEnvPrefixes, func(p string) bool { return strings.HasPrefix(k, p) }) {
				continue
			}
			if _, builder := skip[k]; builder {
				continue
			}
			if cur, set := s.Environment[k]; set && cur != nil {
				continue
			}
			s.Environment[k] = &v
		}
		project.Services[name] = s
	}
}

// publishOn binds each port that has no host address to addr, also one with
// no host port ("- 8080"), which Docker publishes on a random host port.
func publishOn(project *types.Project, addr string) {
	if addr == "" {
		return
	}
	for name, s := range project.Services {
		for i := range s.Ports {
			if s.Ports[i].HostIP == "" {
				s.Ports[i].HostIP = addr
			}
		}
		project.Services[name] = s
	}
}
