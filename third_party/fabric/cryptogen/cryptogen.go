/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/
// Copied from hyperledger/fabric v3.1.4 (cmd/cryptogen/main.go). See third_party/fabric/README.md.
package cryptogen

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"

	"github.com/luthersystems/fabric-network-builder/third_party/fabric/cryptogen/ca"
	"github.com/luthersystems/fabric-network-builder/third_party/fabric/cryptogen/csp"
	"github.com/luthersystems/fabric-network-builder/third_party/fabric/cryptogen/msp"

	"gopkg.in/yaml.v3"
)

const (
	userBaseName            = "User"
	adminBaseName           = "Admin"
	defaultHostnameTemplate = "{{.Prefix}}{{.Index}}"
	defaultCNTemplate       = "{{.Hostname}}.{{.Domain}}"
	ECDSA                   = "ecdsa"
	ED25519                 = "ed25519"
)

type HostnameData struct {
	Prefix string
	Index  int
	Domain string
}

type SpecData struct {
	Hostname   string
	Domain     string
	CommonName string
}

type NodeTemplate struct {
	Count              int      `yaml:"Count"`
	Start              int      `yaml:"Start"`
	Hostname           string   `yaml:"Hostname"`
	SANS               []string `yaml:"SANS"`
	PublicKeyAlgorithm string   `yaml:"PublicKeyAlgorithm"`
}

type NodeSpec struct {
	isAdmin            bool
	Hostname           string   `yaml:"Hostname"`
	CommonName         string   `yaml:"CommonName"`
	Country            string   `yaml:"Country"`
	Province           string   `yaml:"Province"`
	Locality           string   `yaml:"Locality"`
	OrganizationalUnit string   `yaml:"OrganizationalUnit"`
	StreetAddress      string   `yaml:"StreetAddress"`
	PostalCode         string   `yaml:"PostalCode"`
	SANS               []string `yaml:"SANS"`
	PublicKeyAlgorithm string   `yaml:"PublicKeyAlgorithm"`
}

type UsersSpec struct {
	Count              int    `yaml:"Count"`
	PublicKeyAlgorithm string `yaml:"PublicKeyAlgorithm"`
}

type OrgSpec struct {
	Name          string       `yaml:"Name"`
	Domain        string       `yaml:"Domain"`
	EnableNodeOUs bool         `yaml:"EnableNodeOUs"`
	CA            NodeSpec     `yaml:"CA"`
	Template      NodeTemplate `yaml:"Template"`
	Specs         []NodeSpec   `yaml:"Specs"`
	Users         UsersSpec    `yaml:"Users"`
}

type Config struct {
	OrdererOrgs []OrgSpec `yaml:"OrdererOrgs"`
	PeerOrgs    []OrgSpec `yaml:"PeerOrgs"`
}

// outputDirFlag, inputDirFlag and configData replace the command line flags.
var (
	outputDirFlag string
	inputDirFlag  string
	configData    string
)

func getConfig() (*Config, error) {
	config := &Config{}
	err := yaml.Unmarshal([]byte(configData), &config)
	if err != nil {
		return nil, fmt.Errorf("Error Unmarshalling YAML: %s", err)
	}

	return config, nil
}

func extend() {
	config, err := getConfig()
	if err != nil {
		panic(fmt.Errorf("Error reading config: %s", err))
	}

	for _, orgSpec := range config.PeerOrgs {
		err = renderOrgSpec(&orgSpec, "peer")
		if err != nil {
			panic(fmt.Errorf("Error processing peer configuration: %s", err))
		}
		extendPeerOrg(orgSpec)
	}

	for _, orgSpec := range config.OrdererOrgs {
		err = renderOrgSpec(&orgSpec, "orderer")
		if err != nil {
			panic(fmt.Errorf("Error processing orderer configuration: %s", err))
		}
		extendOrdererOrg(orgSpec)
	}
}

func extendPeerOrg(orgSpec OrgSpec) {
	orgName := orgSpec.Domain
	orgDir := filepath.Join(inputDirFlag, "peerOrganizations", orgName)
	if _, err := os.Stat(orgDir); os.IsNotExist(err) {
		generatePeerOrg(inputDirFlag, orgSpec)
		return
	}

	peersDir := filepath.Join(orgDir, "peers")
	usersDir := filepath.Join(orgDir, "users")
	caDir := filepath.Join(orgDir, "ca")
	tlscaDir := filepath.Join(orgDir, "tlsca")

	signCA := getCA(caDir, orgSpec, orgSpec.CA.CommonName)
	tlsCA := getCA(tlscaDir, orgSpec, "tls"+orgSpec.CA.CommonName)

	generateNodes(peersDir, orgSpec.Specs, signCA, tlsCA, msp.PEER, orgSpec.EnableNodeOUs)

	adminUser := NodeSpec{
		isAdmin:            true,
		CommonName:         fmt.Sprintf("%s@%s", adminBaseName, orgName),
		PublicKeyAlgorithm: ECDSA,
	}
	// copy the admin cert to each of the org's peer's MSP admincerts
	for _, spec := range orgSpec.Specs {
		if orgSpec.EnableNodeOUs {
			continue
		}
		err := copyAdminCert(usersDir,
			filepath.Join(peersDir, spec.CommonName, "msp", "admincerts"), adminUser.CommonName)
		if err != nil {
			panic(fmt.Errorf("Error copying admin cert for org %s peer %s: %v",
				orgName, spec.CommonName, err))
		}
	}

	publicKeyAlg := getPublicKeyAlg(orgSpec.Users.PublicKeyAlgorithm)
	// TODO: add ability to specify usernames
	users := []NodeSpec{}
	for j := 1; j <= orgSpec.Users.Count; j++ {
		user := NodeSpec{
			CommonName:         fmt.Sprintf("%s%d@%s", userBaseName, j, orgName),
			PublicKeyAlgorithm: publicKeyAlg,
		}

		users = append(users, user)
	}

	generateNodes(usersDir, users, signCA, tlsCA, msp.CLIENT, orgSpec.EnableNodeOUs)
}

func extendOrdererOrg(orgSpec OrgSpec) {
	orgName := orgSpec.Domain

	orgDir := filepath.Join(inputDirFlag, "ordererOrganizations", orgName)
	caDir := filepath.Join(orgDir, "ca")
	usersDir := filepath.Join(orgDir, "users")
	tlscaDir := filepath.Join(orgDir, "tlsca")
	orderersDir := filepath.Join(orgDir, "orderers")
	if _, err := os.Stat(orgDir); os.IsNotExist(err) {
		generateOrdererOrg(inputDirFlag, orgSpec)
		return
	}

	signCA := getCA(caDir, orgSpec, orgSpec.CA.CommonName)
	tlsCA := getCA(tlscaDir, orgSpec, "tls"+orgSpec.CA.CommonName)

	generateNodes(orderersDir, orgSpec.Specs, signCA, tlsCA, msp.ORDERER, orgSpec.EnableNodeOUs)

	adminUser := NodeSpec{
		isAdmin:            true,
		CommonName:         fmt.Sprintf("%s@%s", adminBaseName, orgName),
		PublicKeyAlgorithm: ECDSA,
	}

	for _, spec := range orgSpec.Specs {
		if orgSpec.EnableNodeOUs {
			continue
		}
		err := copyAdminCert(usersDir,
			filepath.Join(orderersDir, spec.CommonName, "msp", "admincerts"), adminUser.CommonName)
		if err != nil {
			panic(fmt.Errorf("Error copying admin cert for org %s orderer %s: %v",
				orgName, spec.CommonName, err))
		}
	}
}

func generate() {
	config, err := getConfig()
	if err != nil {
		panic(fmt.Errorf("Error reading config: %s", err))
	}

	for _, orgSpec := range config.PeerOrgs {
		err = renderOrgSpec(&orgSpec, "peer")
		if err != nil {
			panic(fmt.Errorf("Error processing peer configuration: %s", err))
		}
		generatePeerOrg(outputDirFlag, orgSpec)
	}

	for _, orgSpec := range config.OrdererOrgs {
		err = renderOrgSpec(&orgSpec, "orderer")
		if err != nil {
			panic(fmt.Errorf("Error processing orderer configuration: %s", err))
		}
		generateOrdererOrg(outputDirFlag, orgSpec)
	}
}

func parseTemplate(input string, data any) (string, error) {
	t, err := template.New("parse").Parse(input)
	if err != nil {
		return "", fmt.Errorf("Error parsing template: %s", err)
	}

	output := new(bytes.Buffer)
	err = t.Execute(output, data)
	if err != nil {
		return "", fmt.Errorf("Error executing template: %s", err)
	}

	return output.String(), nil
}

func parseTemplateWithDefault(input, defaultInput string, data any) (string, error) {
	// Use the default if the input is an empty string
	if len(input) == 0 {
		input = defaultInput
	}

	return parseTemplate(input, data)
}

func renderNodeSpec(domain string, spec *NodeSpec) error {
	data := SpecData{
		Hostname: spec.Hostname,
		Domain:   domain,
	}

	// Process our CommonName
	cn, err := parseTemplateWithDefault(spec.CommonName, defaultCNTemplate, data)
	if err != nil {
		return err
	}

	spec.CommonName = cn
	data.CommonName = cn

	if spec.PublicKeyAlgorithm == "" {
		spec.PublicKeyAlgorithm = ECDSA
	}

	// Save off our original, unprocessed SANS entries
	origSANS := spec.SANS

	// Set our implicit SANS entries for CN/Hostname
	spec.SANS = []string{cn, spec.Hostname}

	// Finally, process any remaining SANS entries
	for _, _san := range origSANS {
		san, err := parseTemplate(_san, data)
		if err != nil {
			return err
		}

		spec.SANS = append(spec.SANS, san)
	}

	return nil
}

func renderOrgSpec(orgSpec *OrgSpec, prefix string) error {
	publickKeyAlg := getPublicKeyAlg(orgSpec.Template.PublicKeyAlgorithm)
	// First process all of our templated nodes
	for i := 0; i < orgSpec.Template.Count; i++ {
		data := HostnameData{
			Prefix: prefix,
			Index:  i + orgSpec.Template.Start,
			Domain: orgSpec.Domain,
		}

		hostname, err := parseTemplateWithDefault(orgSpec.Template.Hostname, defaultHostnameTemplate, data)
		if err != nil {
			return err
		}

		spec := NodeSpec{
			Hostname:           hostname,
			SANS:               orgSpec.Template.SANS,
			PublicKeyAlgorithm: publickKeyAlg,
		}
		orgSpec.Specs = append(orgSpec.Specs, spec)
	}

	// Touch up all general node-specs to add the domain
	for idx, spec := range orgSpec.Specs {
		err := renderNodeSpec(orgSpec.Domain, &spec)
		if err != nil {
			return err
		}

		orgSpec.Specs[idx] = spec
	}

	// Process the CA node-spec in the same manner
	if len(orgSpec.CA.Hostname) == 0 {
		orgSpec.CA.Hostname = "ca"
	}
	err := renderNodeSpec(orgSpec.Domain, &orgSpec.CA)
	if err != nil {
		return err
	}

	return nil
}

func generatePeerOrg(baseDir string, orgSpec OrgSpec) {
	orgName := orgSpec.Domain

	fmt.Println(orgName)
	// generate CAs
	orgDir := filepath.Join(baseDir, "peerOrganizations", orgName)
	caDir := filepath.Join(orgDir, "ca")
	tlsCADir := filepath.Join(orgDir, "tlsca")
	mspDir := filepath.Join(orgDir, "msp")
	peersDir := filepath.Join(orgDir, "peers")
	usersDir := filepath.Join(orgDir, "users")
	adminCertsDir := filepath.Join(mspDir, "admincerts")
	// generate signing CA
	signCA, err := ca.NewCA(caDir, orgName, orgSpec.CA.CommonName, orgSpec.CA.Country, orgSpec.CA.Province, orgSpec.CA.Locality, orgSpec.CA.OrganizationalUnit, orgSpec.CA.StreetAddress, orgSpec.CA.PostalCode, orgSpec.CA.PublicKeyAlgorithm)
	if err != nil {
		panic(fmt.Errorf("Error generating signCA for org %s: %v", orgName, err))
	}
	// generate TLS CA
	tlsCA, err := ca.NewCA(tlsCADir, orgName, "tls"+orgSpec.CA.CommonName, orgSpec.CA.Country, orgSpec.CA.Province, orgSpec.CA.Locality, orgSpec.CA.OrganizationalUnit, orgSpec.CA.StreetAddress, orgSpec.CA.PostalCode, orgSpec.CA.PublicKeyAlgorithm)
	if err != nil {
		panic(fmt.Errorf("Error generating tlsCA for org %s: %v", orgName, err))
	}

	err = msp.GenerateVerifyingMSP(mspDir, signCA, tlsCA, orgSpec.EnableNodeOUs, orgSpec.CA.PublicKeyAlgorithm)
	if err != nil {
		panic(fmt.Errorf("Error generating MSP for org %s: %v", orgName, err))
	}

	generateNodes(peersDir, orgSpec.Specs, signCA, tlsCA, msp.PEER, orgSpec.EnableNodeOUs)

	publicKeyAlg := getPublicKeyAlg(orgSpec.Users.PublicKeyAlgorithm)
	// TODO: add ability to specify usernames
	users := []NodeSpec{}
	for j := 1; j <= orgSpec.Users.Count; j++ {
		user := NodeSpec{
			CommonName:         fmt.Sprintf("%s%d@%s", userBaseName, j, orgName),
			PublicKeyAlgorithm: publicKeyAlg,
		}

		users = append(users, user)
	}
	// add an admin user
	adminUser := NodeSpec{
		isAdmin:            true,
		CommonName:         fmt.Sprintf("%s@%s", adminBaseName, orgName),
		PublicKeyAlgorithm: ECDSA,
	}

	users = append(users, adminUser)
	generateNodes(usersDir, users, signCA, tlsCA, msp.CLIENT, orgSpec.EnableNodeOUs)

	// copy the admin cert to the org's MSP admincerts
	if !orgSpec.EnableNodeOUs {
		err = copyAdminCert(usersDir, adminCertsDir, adminUser.CommonName)
		if err != nil {
			panic(fmt.Errorf("Error copying admin cert for org %s: %v",
				orgName, err))
		}
	}

	// copy the admin cert to each of the org's peer's MSP admincerts
	for _, spec := range orgSpec.Specs {
		if orgSpec.EnableNodeOUs {
			continue
		}
		err = copyAdminCert(usersDir,
			filepath.Join(peersDir, spec.CommonName, "msp", "admincerts"), adminUser.CommonName)
		if err != nil {
			panic(fmt.Errorf("Error copying admin cert for org %s peer %s: %v",
				orgName, spec.CommonName, err))
		}
	}
}

func copyAdminCert(usersDir, adminCertsDir, adminUserName string) error {
	if _, err := os.Stat(filepath.Join(adminCertsDir,
		adminUserName+"-cert.pem")); err == nil {
		return nil
	}
	// delete the contents of admincerts
	err := os.RemoveAll(adminCertsDir)
	if err != nil {
		return err
	}
	// recreate the admincerts directory
	err = os.MkdirAll(adminCertsDir, 0o755)
	if err != nil {
		return err
	}
	err = copyFile(filepath.Join(usersDir, adminUserName, "msp", "signcerts",
		adminUserName+"-cert.pem"), filepath.Join(adminCertsDir,
		adminUserName+"-cert.pem"))
	if err != nil {
		return err
	}
	return nil
}

func generateNodes(baseDir string, nodes []NodeSpec, signCA *ca.CA, tlsCA *ca.CA, nodeType int, nodeOUs bool) {
	for _, node := range nodes {
		nodeDir := filepath.Join(baseDir, node.CommonName)
		if _, err := os.Stat(nodeDir); os.IsNotExist(err) {
			currentNodeType := nodeType
			if node.isAdmin && nodeOUs {
				currentNodeType = msp.ADMIN
			}
			err := msp.GenerateLocalMSP(nodeDir, node.CommonName, node.SANS, signCA, tlsCA, currentNodeType, nodeOUs, node.PublicKeyAlgorithm)
			if err != nil {
				panic(fmt.Errorf("Error generating local MSP for %v: %v", node, err))
			}
		}
	}
}

func generateOrdererOrg(baseDir string, orgSpec OrgSpec) {
	orgName := orgSpec.Domain

	// generate CAs
	orgDir := filepath.Join(baseDir, "ordererOrganizations", orgName)
	caDir := filepath.Join(orgDir, "ca")
	tlsCADir := filepath.Join(orgDir, "tlsca")
	mspDir := filepath.Join(orgDir, "msp")
	orderersDir := filepath.Join(orgDir, "orderers")
	usersDir := filepath.Join(orgDir, "users")
	adminCertsDir := filepath.Join(mspDir, "admincerts")
	// generate signing CA
	signCA, err := ca.NewCA(caDir, orgName, orgSpec.CA.CommonName, orgSpec.CA.Country, orgSpec.CA.Province, orgSpec.CA.Locality, orgSpec.CA.OrganizationalUnit, orgSpec.CA.StreetAddress, orgSpec.CA.PostalCode, orgSpec.CA.PublicKeyAlgorithm)
	if err != nil {
		panic(fmt.Errorf("Error generating signCA for org %s: %v", orgName, err))
	}
	// generate TLS CA
	tlsCA, err := ca.NewCA(tlsCADir, orgName, "tls"+orgSpec.CA.CommonName, orgSpec.CA.Country, orgSpec.CA.Province, orgSpec.CA.Locality, orgSpec.CA.OrganizationalUnit, orgSpec.CA.StreetAddress, orgSpec.CA.PostalCode, orgSpec.CA.PublicKeyAlgorithm)
	if err != nil {
		panic(fmt.Errorf("Error generating tlsCA for org %s: %v", orgName, err))
	}

	err = msp.GenerateVerifyingMSP(mspDir, signCA, tlsCA, orgSpec.EnableNodeOUs, orgSpec.CA.PublicKeyAlgorithm)
	if err != nil {
		panic(fmt.Errorf("Error generating MSP for org %s: %v", orgName, err))
	}

	generateNodes(orderersDir, orgSpec.Specs, signCA, tlsCA, msp.ORDERER, orgSpec.EnableNodeOUs)

	adminUser := NodeSpec{
		isAdmin:            true,
		CommonName:         fmt.Sprintf("%s@%s", adminBaseName, orgName),
		PublicKeyAlgorithm: ECDSA,
	}

	// generate an admin for the orderer org
	users := []NodeSpec{}
	// add an admin user
	users = append(users, adminUser)
	generateNodes(usersDir, users, signCA, tlsCA, msp.CLIENT, orgSpec.EnableNodeOUs)

	// copy the admin cert to the org's MSP admincerts
	if !orgSpec.EnableNodeOUs {
		err = copyAdminCert(usersDir, adminCertsDir, adminUser.CommonName)
		if err != nil {
			panic(fmt.Errorf("Error copying admin cert for org %s: %v",
				orgName, err))
		}
	}

	// copy the admin cert to each of the org's orderers's MSP admincerts
	for _, spec := range orgSpec.Specs {
		if orgSpec.EnableNodeOUs {
			continue
		}
		err = copyAdminCert(usersDir,
			filepath.Join(orderersDir, spec.CommonName, "msp", "admincerts"), adminUser.CommonName)
		if err != nil {
			panic(fmt.Errorf("Error copying admin cert for org %s orderer %s: %v",
				orgName, spec.CommonName, err))
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	cerr := out.Close()
	if err != nil {
		return err
	}
	return cerr
}

func getCA(caDir string, spec OrgSpec, name string) *ca.CA {
	priv, _ := csp.LoadPrivateKey(caDir)
	cert, _ := ca.LoadCertificate(caDir)

	return &ca.CA{
		Name:               name,
		Signer:             ca.GetSignerFromPrivateKey(priv),
		SignCert:           cert,
		Country:            spec.CA.Country,
		Province:           spec.CA.Province,
		Locality:           spec.CA.Locality,
		OrganizationalUnit: spec.CA.OrganizationalUnit,
		StreetAddress:      spec.CA.StreetAddress,
		PostalCode:         spec.CA.PostalCode,
	}
}

func getPublicKeyAlg(pubAlgFromConfig string) (publicKeyAlg string) {
	if pubAlgFromConfig == "" {
		publicKeyAlg = ECDSA
	} else {
		publicKeyAlg = pubAlgFromConfig
	}
	return
}
