// Package cli is the fabric-network-builder command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/luthersystems/fabric-network-builder/internal/admin"
	"github.com/luthersystems/fabric-network-builder/internal/certs"
	"github.com/luthersystems/fabric-network-builder/internal/chaincode"
	"github.com/luthersystems/fabric-network-builder/internal/compose"
	"github.com/luthersystems/fabric-network-builder/internal/fabtools"
	"github.com/luthersystems/fabric-network-builder/internal/network"
	"github.com/luthersystems/fabric-network-builder/internal/render"
	"github.com/luthersystems/fabric-network-builder/internal/topology"
)

// env is one run of the command line.
type env struct {
	ctx     context.Context
	in      io.Reader
	out     io.Writer
	owner   *owner
	v       *values
	pos     map[string]string
	dir     string
	channel string
	storage string
	force   bool
}

// Streams are the standard streams of a run.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Run runs the command line args and returns the exit code.
func Run(ctx context.Context, args []string, s Streams) int {
	in, out, errOut := s.In, s.Out, s.Err
	inv, err := parse(globals, commands, args)
	var help errHelp
	if errors.As(err, &help) {
		printUsage(out, globals, commands, help.cmd)
		return 0
	}
	var uerr *usageError
	if errors.As(err, &uerr) {
		printUsage(errOut, globals, commands, uerr.cmd)
		_, _ = fmt.Fprintf(errOut, "%s: error: %s\n", prog, uerr.msg)
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "%s: error: %v\n", prog, err)
		return 2
	}
	cmd, v := inv.cmd, inv.v
	o, err := parseChown(v.str("chown", ""))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "%s: error: %v\n", prog, err)
		return 2
	}
	e := &env{
		ctx:     ctx,
		dir:     ".",
		in:      in,
		out:     out,
		channel: v.str("channel", "luther"),
		force:   v.boolean("force", false),
		storage: v.str("storage", ""),
		owner:   o,
		v:       v,
		pos:     inv.pos,
	}
	run := map[*command]func() error{
		cmdGenerate:    e.generate,
		cmdExtend:      e.extend,
		cmdUp:          e.up,
		cmdInstall:     e.install,
		cmdGeneratecc:  e.generatecc,
		cmdDown:        e.down,
		cmdCertExpires: e.certExpiries,
		cmdReissue:     e.reissue,
	}[cmd]
	if err = run(); err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			printUsage(errOut, globals, commands, ue.cmd)
			_, _ = fmt.Fprintf(errOut, "%s: error: %s\n", prog, ue.msg)
			return 2
		}
		_, _ = fmt.Fprintf(errOut, "%s: error: %v\n", prog, err)
		return 1
	}
	return 0
}

func (e *env) printf(format string, args ...any) { _, _ = fmt.Fprintf(e.out, format, args...) }

// announce prints what the command does and asks to go on, as byfn.sh did
// before each step it ran.
func (e *env) announce(what string) error {
	msg := fmt.Sprintf("%s with channel '%s'", what, e.channel)
	if e.storage == "couchdb" {
		msg += " and using couchdb"
	}
	e.printf("%s\n", msg)
	return askProceed(e.force, e.in, e.out)
}

func (e *env) path(rel string) string { return filepath.Join(e.dir, rel) }

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// archiveNames are the paths a --archive tar.xz holds.
var archiveNames = []string{"crypto-config", "crypto-config.yaml", "configtx.yaml", "channel-artifacts", "scripts"}

// cryptoAssets are the paths --chown changes after cryptogen.
var cryptoAssets = []string{"crypto-config", "channel-artifacts", "docker-compose-e2e.yaml"}

func (e *env) topologyOptions() topology.Options {
	v := e.v
	o := topology.DefaultOptions()
	o.CCName = v.str("cc_name", o.CCName)
	o.DomainName = v.str("domain_name", o.DomainName)
	o.ConnectDomainName = v.str("connect_domain_name", o.ConnectDomainName)
	o.EnableNodeOUs = v.boolean("enable_node_ous", o.EnableNodeOUs)
	o.OrgCount = v.integer("org_count", o.OrgCount)
	o.PeerCount = v.integer("peer_count", o.PeerCount)
	o.MinEndorsers = v.integer("min_endorsers", o.MinEndorsers)
	o.PrivateStructure = v.str("private_structure", o.PrivateStructure)
	o.ReqPeerCount = v.integer("req_peer_count", o.ReqPeerCount)
	o.MaxPeerCount = v.integer("max_peer_count", o.MaxPeerCount)
	o.ExecuteTimeout = v.integer("execute_timeout", o.ExecuteTimeout)
	o.OrdererType = v.str("orderer_type", o.OrdererType)
	o.OrdererCount = v.integer("orderer_count", o.OrdererCount)
	o.OrdererSANDomains = v.lists["orderer_san_domains"]
	o.PeerSANDomains = v.lists["peer_san_domains"]
	return o
}

// inArchive runs f with e.dir set to a temporary directory under the
// current one, then writes the archive of its result to dest.
func (e *env) inArchive(dest string, f func() error) error {
	tmp, err := os.MkdirTemp(e.dir, "fabric-network")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	orig := e.dir
	e.dir = tmp
	err = f()
	e.dir = orig
	if err != nil {
		return err
	}
	if err := writeArchive(tmp, dest, archiveNames); err != nil {
		return err
	}
	e.printf("wrote %s\n", dest)
	return e.owner.chownAll(".", dest)
}

func (e *env) generate() error {
	opts := e.topologyOptions()
	n, err := topology.New(opts)
	if err != nil {
		return usagef(cmdGenerate, "%v", err)
	}
	if dest := e.v.str("archive_path", ""); dest != "" {
		if e.v.has("template") {
			return usagef(cmdGenerate, "cannot combine --[no-]template and --archive")
		}
		return e.inArchive(dest, func() error { return e.doGenerate(n) })
	}
	return e.doGenerate(n)
}

func (e *env) doGenerate(n *topology.Network) error {
	if !e.force {
		for _, f := range []string{"crypto-config.yaml", "configtx.yaml", "crypto-config", "channel-artifacts"} {
			if exists(e.path(f)) {
				return fmt.Errorf("generated artifacts already exist: %s (use --force to replace them)", f)
			}
		}
	}
	template, set := e.v.bools["template"]
	// Render the template unless --no-template.
	if !set || template {
		files, err := render.Network(n)
		if err != nil {
			return err
		}
		if err := render.Write(e.dir, files, e.owner.chownFunc(), e.out); err != nil {
			return err
		}
	}
	// Generate the crypto material unless --template.
	if set && template {
		return nil
	}
	if err := e.announce("Generating certs and genesis block for"); err != nil {
		return err
	}
	cc := e.path("crypto-config")
	if exists(cc) {
		// Without --force, doGenerate refused above.
		if err := os.RemoveAll(cc); err != nil {
			return err
		}
	}
	e.printf("Generating certificates\n")
	if err := fabtools.GenerateCrypto(e.path("crypto-config.yaml"), cc); err != nil {
		return err
	}
	if err := replacePrivateKey(e.dir, n.DomainName); err != nil {
		return err
	}
	// An existing directory is a forced regenerate: replace it, as
	// crypto-config is replaced.
	if err := os.RemoveAll(e.path("channel-artifacts")); err != nil {
		return err
	}
	e.printf("Generating channel '%s' genesis block\n", e.channel)
	if err := fabtools.ChannelGenesisBlock(e.dir, e.channel, e.path(network.ChannelBlock(e.channel))); err != nil {
		return err
	}
	return e.owner.chownAll(e.dir, cryptoAssets...)
}

func (o *owner) chownFunc() func(string) error {
	if o == nil {
		return nil
	}
	return o.chown
}

func (e *env) extend() error {
	domain := e.v.str("domain_name", topology.DefaultOptions().DomainName)
	if err := e.doExtend(domain); err != nil {
		return err
	}
	dest := e.v.str("archive_path", "")
	if dest == "" {
		return nil
	}
	// The Python builder extended an empty temporary directory, which
	// always failed: there was no crypto-config to extend. Archive the
	// extended network instead.
	if err := writeArchive(e.dir, dest, archiveNames); err != nil {
		return err
	}
	e.printf("wrote %s\n", dest)
	return e.owner.chownAll(".", dest)
}

func (e *env) doExtend(domain string) error {
	if err := e.announce("Extending"); err != nil {
		return err
	}
	cc := e.path("crypto-config")
	if !exists(cc) {
		return errors.New("crypto-config certificate tree does not exist")
	}
	e.printf("Extending certificates\n")
	if err := fabtools.ExtendCrypto(e.path("crypto-config.yaml"), cc); err != nil {
		return err
	}
	if err := replacePrivateKey(e.dir, domain); err != nil {
		return err
	}
	return e.owner.chownAll(e.dir, cryptoAssets...)
}

// composeFiles are the compose files of the network, as byfn.sh chose them.
func (e *env) composeFiles() []string {
	files := []string{"docker-compose-cli.yaml"}
	if exists(e.path("docker-compose-ccaas.yaml")) {
		files = append(files, "docker-compose-ccaas.yaml")
	}
	if e.storage == "couchdb" {
		files = append(files, "docker-compose-couch.yaml")
	}
	return files
}

func projectName() string {
	if n := os.Getenv("COMPOSE_PROJECT_NAME"); n != "" {
		return n
	}
	return "fnb"
}

func (e *env) loadProject(ccVersion string) (*compose.Project, error) {
	envVars, err := composeEnv(e.channel, ccVersion)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(e.dir)
	if err != nil {
		return nil, err
	}
	return compose.Load(e.ctx, compose.Options{
		Dir:   dir,
		Name:  projectName(),
		Files: e.composeFiles(),
		Env:   envVars,
		Out:   e.out,
	})
}

func (e *env) up() error {
	if err := e.announce("Starting"); err != nil {
		return err
	}
	if !exists(e.path("crypto-config")) {
		return errors.New("missing crypto material: run generate first")
	}
	p, err := e.loadProject(e.v.str("cc_version", ""))
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()
	if err = p.Up(e.ctx); err != nil {
		return fmt.Errorf("unable to start network: %w", err)
	}
	err = e.withAdmin(p, func(n *admin.Network) error {
		if cerr := n.CreateChannel(e.ctx, e.channel); cerr != nil {
			return fmt.Errorf("unable to create channel %s: %w", e.channel, cerr)
		}
		if jerr := n.JoinPeers(e.ctx, e.channel); jerr != nil {
			return fmt.Errorf("unable to join channel %s: %w", e.channel, jerr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.printf("Network joined channel %s and is ready for chaincode installation\n", e.channel)
	return nil
}

// withAdmin runs f with an admin client of the running network. The route
// to the nodes is open only while f runs: in a container, the builder
// leaves the network after, so a later `down` can remove it.
func (e *env) withAdmin(p *compose.Project, f func(*admin.Network) error) error {
	route, err := p.Route(e.ctx)
	if err != nil {
		return err
	}
	defer route.Close()
	n, err := admin.New(e.dir, route.Resolve, e.out)
	if err != nil {
		return err
	}
	defer n.Close()
	return f(n)
}

// localPath maps a chaincode path the consumers pass as the cli
// container saw it (/chaincodes/x.tar.gz) to the project directory
// (./chaincodes/x.tar.gz), as generatecc.sh did with REL_PATH.
func (e *env) localPath(p string) string {
	if filepath.IsAbs(p) {
		return e.path("." + p)
	}
	return e.path(p)
}

// ccArgs are the positionals of install and generatecc.
type ccArgs struct {
	version string
	// path is the package path in the project directory.
	path  string
	names []string
}

func (e *env) chaincodeArgs(cmd *command) (ccArgs, error) {
	names := strings.Fields(e.pos["cc_variants"])
	if len(names) == 0 {
		// The callers act on an empty list (generatecc skips, install
		// refuses) before they need the other arguments, as byfn.sh did.
		return ccArgs{}, nil
	}
	var missing []string
	for _, k := range []string{"cc_name", "cc_version", "cc_path"} {
		if e.pos[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return ccArgs{}, usagef(cmd, "empty %s", strings.Join(missing, ", "))
	}
	return ccArgs{
		version: e.pos["cc_version"],
		path:    e.localPath(e.pos["cc_path"]),
		names:   names,
	}, nil
}

func (e *env) install() error {
	if err := e.announce("Installing chaincode tar.gz package"); err != nil {
		return err
	}
	a, err := e.chaincodeArgs(cmdInstall)
	if err != nil {
		return err
	}
	if len(a.names) == 0 {
		return usagef(cmdInstall, "empty cc_variants: no chaincode names")
	}
	p, err := e.loadProject("")
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()
	return e.withAdmin(p, func(n *admin.Network) error {
		return n.Install(e.ctx, admin.InstallOptions{
			Channel:      e.channel,
			Names:        a.names,
			Version:      a.version,
			PackageDir:   filepath.Dir(a.path),
			InitRequired: e.v.boolean("init_required", false),
		})
	})
}

// defaultCCaaSImage is the image of a CCaaS service; compose fills
// CHAINCODE_VERSION from up --cc-version.
const defaultCCaaSImage = "luthersystems/substrate:$CHAINCODE_VERSION"

func parseImageOverrides(pairs, names []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		name, image, ok := strings.Cut(p, "=")
		if !ok || name == "" || image == "" {
			return nil, fmt.Errorf("--image-override expects NAME=IMAGE, got %q", p)
		}
		found := false
		for _, n := range names {
			found = found || n == name
		}
		if !found {
			return nil, fmt.Errorf("--image-override NAME %q is not in cc_variants (%s)", name, strings.Join(names, ", "))
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--image-override NAME %q specified more than once", name)
		}
		out[name] = image
	}
	return out, nil
}

func (e *env) generatecc() error {
	ccaas := e.v.boolean("ccaas", false)
	overrides := e.v.lists["image_override"]
	if len(overrides) > 0 && !ccaas {
		return usagef(cmdGeneratecc, "--image-override requires --ccaas")
	}
	if err := e.announce("Generating chaincode package"); err != nil {
		return err
	}
	a, err := e.chaincodeArgs(cmdGeneratecc)
	if err != nil {
		return err
	}
	names := a.names
	if len(names) == 0 {
		e.printf("No chaincodes to generate, skipping...\n")
		e.printf("skipping ccaas compose file...\n")
		return nil
	}
	images, err := parseImageOverrides(overrides, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		pkg, berr := chaincode.Build(a.path, name, a.version, ccaas)
		if berr != nil {
			return berr
		}
		if err = pkg.Write(); err != nil {
			return err
		}
		e.printf("generated %s: %s\n", pkg.Path, pkg.ID)
	}
	// The Python builder wrote the compose file for every generatecc, Go
	// chaincode too; consumers run the CCaaS generatecc last, which
	// replaces it.
	var services []render.CCaaSService
	for i, name := range names {
		image := defaultCCaaSImage
		if img, ok := images[name]; ok {
			image = img
		}
		services = append(services, render.CCaaSService{
			ServiceName: name + "-peer0",
			CCIDEnvVar:  "CCID_" + strings.ToUpper(name),
			Port:        9080 + i,
			Image:       image,
		})
	}
	b, err := render.CCaaSCompose(services)
	if err != nil {
		return err
	}
	dest := e.path("docker-compose-ccaas.yaml")
	if err = os.WriteFile(dest, b, 0o644); err != nil { //nolint:gosec // compose file
		return err
	}
	return e.owner.chown(dest)
}

func (e *env) down() error {
	if err := e.announce("Stopping"); err != nil {
		return err
	}
	p, err := e.loadProject("")
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()
	if err = p.Down(e.ctx); err != nil {
		return err
	}
	if err = os.RemoveAll(e.path("ledgers-backup")); err != nil {
		return err
	}
	return p.RemoveChaincodeContainers(e.ctx)
}

func (e *env) certExpiries() error {
	list, err := certs.Expiries(e.path("crypto-config"))
	if err != nil {
		return err
	}
	for _, x := range list {
		e.printf("%s\t%s\n", x.Expiry, x.Path)
	}
	return nil
}

func (e *env) reissue() error {
	v := e.v
	_, daysSet := v.ints["days"]
	return certs.Reissue(certs.ReissueOptions{
		CryptoConfig: v.str("crypto_config", "crypto-config"),
		Type:         v.str("type", "both"),
		Nodes:        v.lists["node"],
		AllExpired:   v.boolean("all_expired", false),
		All:          v.boolean("all", false),
		Days:         v.integer("days", 0),
		DaysSet:      daysSet,
		DryRun:       v.boolean("dry_run", false),
		NoBackup:     v.boolean("no_backup", false),
	}, e.out)
}
