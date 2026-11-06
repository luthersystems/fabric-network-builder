package chaincode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func untar(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		assert.True(t, h.ModTime.Equal(mtime), h.Name)
		out[h.Name] = body
	}
}

func TestCCaaSPackage(t *testing.T) {
	dir := t.TempDir()
	p, err := Build(filepath.Join(dir, "substrate01-1.0.tar.gz"), "a-b", "1.2.3", true)
	require.NoError(t, err)
	assert.Equal(t, "a-b-1.2.3", p.Label)
	assert.Equal(t, filepath.Join(dir, "a-b-1.2.3.tar.gz"), p.Path)
	sum := sha256.Sum256(p.Bytes)
	assert.Equal(t, "a-b-1.2.3:"+hex.EncodeToString(sum[:]), p.ID)

	files := untar(t, p.Bytes)
	assert.JSONEq(t, `{"path":"main","type":"ccaas","label":"a-b-1.2.3"}`, string(files["metadata.json"]))
	code := untar(t, files["code.tar.gz"])
	assert.Contains(t, string(code["connection.json"]), `"address": "a-b-peer{{.index}}:8080"`)

	again, err := Build(filepath.Join(dir, "x"), "a-b", "1.2.3", true)
	require.NoError(t, err)
	assert.Equal(t, p.ID, again.ID, "same inputs, same package ID")

	require.NoError(t, p.Write())
	id, err := os.ReadFile(p.IDPath)
	require.NoError(t, err)
	assert.Equal(t, p.ID+"\n", string(id))
}

func writeArchive(t *testing.T, path string, gzipped bool, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var gz *gzip.Writer
	if gzipped {
		gz = gzip.NewWriter(&buf)
		w = gz
	}
	tw := tar.NewWriter(w)
	for n, b := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(b))}))
		_, err := tw.Write([]byte(b))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	if gz != nil {
		require.NoError(t, gz.Close())
	}
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
}

func TestGoPackageTakesCodeFromArchive(t *testing.T) {
	for _, gzipped := range []bool{true, false} {
		dir := t.TempDir()
		src := filepath.Join(dir, "cc.tar.gz")
		writeArchive(t, src, gzipped, map[string]string{"metadata.json": "{}", "./code.tar.gz": "CODE"})
		p, err := Build(src, "cc", "v1", false)
		require.NoError(t, err)
		files := untar(t, p.Bytes)
		assert.Equal(t, "CODE", string(files["code.tar.gz"]))
		assert.JSONEq(t, `{"path":"main","type":"golang","label":"cc-v1"}`, string(files["metadata.json"]))
	}
}

func TestGoPackageErrors(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cc.tar.gz")
	writeArchive(t, src, true, map[string]string{"other": "x"})
	_, err := Build(src, "cc", "v1", false)
	require.ErrorContains(t, err, "no code.tar.gz")
	_, err = Build(filepath.Join(dir, "missing.tar.gz"), "cc", "v1", false)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "missing.tar.gz"))
}
