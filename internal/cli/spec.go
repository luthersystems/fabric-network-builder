package cli

// prog is the program name in usage messages.
const prog = "fabric-network-builder"

func opt(k kind, dest, help string, names ...string) *option {
	return &option{names: names, kind: k, dest: dest, help: help}
}

func flag(dest string, value bool, help string, names ...string) *option {
	return &option{names: names, kind: kindBool, dest: dest, value: value, help: help}
}

// globals are the options before the command.
var globals = []*option{
	opt(kindString, "config", "ignored; kept so existing callers still parse", "--config"),
	opt(kindString, "channel", "the channel used on the network (default: luther)", "--channel"),
	opt(kindString, "chown", "set the user:group of generated files", "--chown"),
	flag("force", true, "force the operation without confirmation", "--force", "-f"),
	opt(kindString, "storage", "set the database backend to use (couchdb adds the CouchDB containers)", "--storage", "-s"),
}

var (
	cmdGenerate = &command{
		name: "generate",
		help: "generate a new network",
		options: []*option{
			opt(kindString, "cc_name", "chaincode name (default: com_luthersystems_chaincode_substrate01)", "--cc-name"),
			opt(kindString, "domain_name", "infrastructure domain name (default: example.com)", "--domain-name"),
			opt(kindString, "connect_domain_name", "configured domain name when connecting to peers and orderers", "--connect-domain-name"),
			flag("enable_node_ous", true, "enable node OUs (disabled by default)", "--node-ous"),
			flag("enable_node_ous", false, "disable node OUs (disabled by default)", "--no-node-ous"),
			opt(kindInt, "org_count", "number of organizations (default: 2)", "--org-count"),
			opt(kindInt, "peer_count", "number of peers per organization (default: 2)", "--peer-count"),
			opt(kindInt, "min_endorsers", "minimum number of transaction endorsers (default: 0; -1: a majority)", "--min-endorsers"),
			opt(kindString, "private_structure", "structure of private collections set: shared, nchoose2 or nchoose2common,NAMES (default: shared)", "--private-structure"),
			opt(kindInt, "req_peer_count", "minimum number of private data dissemination peers", "--req-peer-count"),
			opt(kindInt, "max_peer_count", "maximum number of private data dissemination peers", "--max-peer-count"),
			opt(kindInt, "execute_timeout", "chaincode execute timeout in seconds (default: 30)", "--execute-timeout"),
			opt(kindString, "orderer_type", "orderer cluster type: etcdraft", "--orderer-type"),
			opt(kindInt, "orderer_count", "number of orderer servers to generate config for (default: 1)", "--orderer-count"),
			flag("template", true, "only render the network template. do not generate crypto assets", "--template"),
			flag("template", false, "do not render a template. only generate crypto assets", "--no-template"),
			opt(kindString, "archive_path", "generate a tar.xz for distribution", "--archive", "-a"),
			opt(kindList, "orderer_san_domains", "domain suffixes to add to SAN field of orderer certificates", "--orderer-san-domains"),
			opt(kindList, "peer_san_domains", "domain suffixes to add to SAN field of peer certificates ({i} is the org index)", "--peer-san-domains"),
		},
	}
	cmdExtend = &command{
		name: "extend",
		help: "extend an existing network",
		options: []*option{
			opt(kindString, "archive_path", "generate a tar.xz for distribution", "--archive", "-a"),
			opt(kindString, "domain_name", "infrastructure domain name (default: example.com)", "--domain-name"),
		},
	}
	cmdUp = &command{
		name: "up",
		help: "launch a network",
		options: []*option{
			opt(kindString, "log_spec", "ignored, as before; set FABRIC_LOGGING_SPEC instead", "--log-spec"),
			opt(kindString, "cc_version", "chaincode version (for CCaaS): the substrate image tag", "--cc-version"),
		},
	}
	cmdInstall = &command{
		name: "install",
		help: "install a chaincode archive (.tar.gz)",
		options: []*option{
			flag("init_required", true, "set chaincode to require init", "--init-required"),
			opt(kindString, "cc_pkg_name", "ignored, as before", "--cc-pkg-name"),
		},
		positionals: []positional{
			{"cc_name", "chaincode name used to invoke its methods"},
			{"cc_version", "deployment version"},
			{"cc_variants", "deployment variants: the chaincode names to install, space separated"},
			{"cc_path", "path to the packaged chaincode tarball"},
		},
	}
	cmdGeneratecc = &command{
		name: "generatecc",
		help: "generate chaincode archives (.tar.gz)",
		options: []*option{
			flag("ccaas", true, "use chaincode as a service", "--ccaas"),
			{names: []string{"--image-override"}, kind: kindAppend, dest: "image_override", meta: "NAME=IMAGE",
				help: "override image for a CCaaS service (repeatable); NAME must match a chaincode in cc_variants"},
		},
		positionals: []positional{
			{"cc_name", "chaincode name used to invoke its methods"},
			{"cc_version", "deployment version"},
			{"cc_variants", "deployment variants: the chaincode names to package, space separated"},
			{"cc_path", "path to the packaged chaincode tarball"},
		},
	}
	cmdInvoke = &command{
		name: "invoke",
		help: "submit a chaincode transaction and wait until it is committed",
		options: []*option{
			flag("init", true, "invoke the chaincode's Init (for --init-required)", "--init"),
			{names: []string{"--peer"}, kind: kindAppend, dest: "peer", meta: "peerJ.orgI",
				help: "endorse on this peer (repeatable; default: the first peer of each org)"},
		},
		positionals: []positional{
			{"cc_name", "chaincode name"},
			{"ctor", `arguments as the peer CLI's -c took them: {"Args":["fn","arg"]}`},
		},
	}
	cmdAnsibleVars = &command{
		name: "ansible-vars",
		help: "write the network files as Ansible variables for the mars Kubernetes roles",
		options: []*option{
			opt(kindString, "output", "the file to write (default: standard output)", "--output", "-o"),
			opt(kindString, "generate_from", "first generate the network sized by this Ansible env file (num_orgs, num_peers, num_orderers, k8s_fabric_network_domain_root); an existing crypto-config is kept unless --force", "--generate-from"),
		},
	}
	cmdDown        = &command{name: "down", help: "teardown network containers"}
	cmdCertExpires = &command{name: "cert_expiries", help: "print expiration values for certs"}
	cmdReissue     = &command{
		name: "reissue",
		help: "renew (re-sign) expiring leaf certs in place using the on-disk CA",
		options: []*option{
			opt(kindString, "crypto_config", "path to the crypto-config tree (default: crypto-config)", "--crypto-config"),
			opt(kindString, "type", "which leaf cert(s) to reissue: signcert, tls or both (default: both)", "--type"),
			{names: []string{"--node", "--peer"}, kind: kindAppend, dest: "node", meta: "NAME",
				help: "reissue the named node's certs (a short name like peer1 also matches). Repeatable."},
			flag("all_expired", true, "reissue every already-expired cert", "--all-expired"),
			flag("all", true, "reissue every leaf cert", "--all"),
			opt(kindInt, "days", "positive validity in days from now, capped at the CA expiry (default: extend to the CA expiry)", "--days"),
			flag("dry_run", true, "show what would change without writing", "--dry-run"),
			flag("no_backup", true, "do not write .bak copies of replaced certs", "--no-backup"),
		},
	}
	commands = []*command{cmdGenerate, cmdExtend, cmdUp, cmdInstall, cmdInvoke, cmdGeneratecc, cmdAnsibleVars, cmdDown, cmdCertExpires, cmdReissue}
)
