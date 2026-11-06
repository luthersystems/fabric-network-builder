#!/usr/bin/env python3
# Copy the Fabric code this directory holds from a hyperledger/fabric
# checkout. Usage: python3 vendor.py <fabric checkout>. See README.md.
import os
import re
import sys

SRC = sys.argv[1]
DST = os.path.dirname(os.path.abspath(__file__))
MOD = "github.com/luthersystems/fabric-network-builder/third_party/fabric"
TAG = "v3.1.4"
FILES = {
    "internal/cryptogen/ca/ca.go": "cryptogen/ca/ca.go",
    "internal/cryptogen/csp/csp.go": "cryptogen/csp/csp.go",
    "internal/cryptogen/msp/msp.go": "cryptogen/msp/msp.go",
    "internal/configtxgen/encoder/encoder.go": "configtxgen/encoder/encoder.go",
    "internal/configtxgen/genesisconfig/config.go": "configtxgen/genesisconfig/config.go",
    "internal/configtxlator/update/update.go": "configtxlator/update/update.go",
    "internal/pkg/identity/identity.go": "pkg/identity/identity.go",
    "cmd/cryptogen/main.go": "cryptogen/cryptogen.go",
}
IMPORTS = {
    "github.com/hyperledger/fabric/internal/cryptogen/": MOD + "/cryptogen/",
    "github.com/hyperledger/fabric/internal/configtxgen/": MOD + "/configtxgen/",
    "github.com/hyperledger/fabric/internal/configtxlator/": MOD + "/configtxlator/",
    "github.com/hyperledger/fabric/internal/pkg/": MOD + "/pkg/",
}


def cryptogen_main(t):
    t = t.replace("package main\n", "package cryptogen\n", 1)
    t = t.replace('\t"gopkg.in/alecthomas/kingpin.v2"\n', "")
    t = t.replace('\t"' + MOD + '/cryptogen/metadata"\n', "")
    t = re.sub(r"var defaultConfig = `.*?\n`\n\n", "", t, flags=re.S)
    t = re.sub(
        r"// command line flags\nvar \(.*?\n\)\n\nfunc main\(\) \{.*?\n\}\n",
        "// outputDirFlag, inputDirFlag and configData replace the command line flags.\n"
        "var (\n\toutputDirFlag string\n\tinputDirFlag  string\n\tconfigData    string\n)\n",
        t, flags=re.S)
    t = re.sub(r"func printVersion\(\) \{.*?\n\}\n\n", "", t, flags=re.S)
    t = re.sub(r"func getConfig\(\) \(\*Config, error\) \{.*?\n\tconfig := &Config\{\}",
               "func getConfig() (*Config, error) {\n\tconfig := &Config{}", t, flags=re.S)
    t = re.sub(
        r"fmt\.Printf\(((?:[^()]|\([^()]*\))*?)\)\n\s*os\.Exit\(-?\d\)",
        lambda m: "panic(fmt.Errorf(" + m.group(1).replace('\\n"', '"').replace(":\\n", ": ") + "))",
        t, flags=re.S)
    t = t.replace("*outputDir", "outputDirFlag").replace("*inputDir", "inputDirFlag")
    if "os.Exit" in t or "kingpin" in t:
        sys.exit("cryptogen main.go changed shape; update vendor.py")
    return t


for src, dst in FILES.items():
    with open(os.path.join(SRC, src)) as f:
        t = f.read()
    for a, b in IMPORTS.items():
        t = t.replace(a, b)
    if src == "cmd/cryptogen/main.go":
        t = cryptogen_main(t)
    note = "// Copied from hyperledger/fabric %s (%s). See third_party/fabric/README.md.\n" % (TAG, src)
    t = t.replace("*/\n", "*/\n" + note, 1)
    out = os.path.join(DST, dst)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with open(out, "w") as f:
        f.write(t)
