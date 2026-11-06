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
	"github.com/docker/compose/v2/pkg/api"
	composesvc "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
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
	Dir   string
	Name  string
	Files []string
}

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
	return &Project{
		dockerCli: dockerCli,
		service:   composesvc.NewComposeService(dockerCli),
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
			AssumeYes:            true,
		},
		Start: api.StartOptions{Project: p.project},
	})
}

// Down stops and removes the containers, networks and volumes, as
// `docker compose down --volumes`.
func (p *Project) Down(ctx context.Context) error {
	return p.service.Down(ctx, p.project.Name, api.DownOptions{Project: p.project, Volumes: true})
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
	c, err := p.client().ContainerInspect(ctx, host)
	if cerrdefs.IsNotFound(err) {
		// A container with another host name, or another daemon.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return c.ID, nil
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
	netName, err := p.NetworkName()
	if err != nil {
		return nil, err
	}
	leave := func() {}
	self, err := p.selfContainer(ctx)
	if err != nil {
		return nil, err
	}
	if self != "" {
		c, ierr := p.client().ContainerInspect(ctx, self)
		if ierr != nil {
			return nil, ierr
		}
		if _, ok := c.NetworkSettings.Networks[netName]; !ok {
			if err = p.client().NetworkConnect(ctx, netName, self, &network.EndpointSettings{}); err != nil {
				return nil, fmt.Errorf("join network %s: %w", netName, err)
			}
			leave = func() {
				// Use a fresh context: the caller's may be done.
				_ = p.client().NetworkDisconnect(context.Background(), netName, self, true)
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
			ip = host
			if c, err := p.client().ContainerInspect(ctx, host); err == nil {
				if ep, ok := c.NetworkSettings.Networks[netName]; ok && ep.IPAddress != "" {
					ip = ep.IPAddress
				}
			}
			ips[host] = ip
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
	cs, err := p.client().ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("name", "dev-peer"))})
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cs {
		if !slices.ContainsFunc(c.Names, devPeer.MatchString) {
			continue
		}
		_, _ = fmt.Fprintf(p.out, "removing container %s\n", strings.TrimPrefix(c.Names[0], "/"))
		errs = append(errs, p.client().ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}))
	}
	imgs, err := p.client().ImageList(ctx, image.ListOptions{})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, img := range imgs {
		if !slices.ContainsFunc(img.RepoTags, devPeer.MatchString) {
			continue
		}
		_, _ = fmt.Fprintf(p.out, "removing image %s\n", img.RepoTags[0])
		_, err := p.client().ImageRemove(ctx, img.ID, image.RemoveOptions{Force: true})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Close releases the Docker client.
func (p *Project) Close() error { return p.client().Close() }
