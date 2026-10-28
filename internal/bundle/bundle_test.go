package bundle

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func sample() *Bundle {
	return &Bundle{
		Manifest: Manifest{ShareID: NewShareID(), Owner: "me@x", Tool: "claude", Title: "T", CreatedAt: time.Now().UTC()},
		Handoff:  []byte("# Handoff"),
		Native:   []byte(`{"type":"user"}` + "\n"),
	}
}

func TestWriteAndReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b := sample()
	path, err := Write(b, dir, filepath.Join(dir, ".staging"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest.Owner != "me@x" || string(got.Handoff) != "# Handoff" || string(got.Native) != string(b.Native) {
		t.Errorf("round trip changed the bundle: %+v", got.Manifest)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, ".staging")); len(entries) != 0 {
		t.Error("staging file left behind")
	}
}

func TestReadRejectsRenamedAndTamperedBundles(t *testing.T) {
	dir := t.TempDir()
	b := sample()
	path, err := Write(b, dir, filepath.Join(dir, ".staging"))
	if err != nil {
		t.Fatal(err)
	}

	renamed := filepath.Join(dir, "aaaaaaaaaa"+Extension)
	data, _ := os.ReadFile(path)
	_ = os.WriteFile(renamed, data, 0o644)
	if _, err := Read(renamed); err == nil {
		t.Error("accepted a bundle under another share ID")
	}

	conflict := strings.TrimSuffix(path, Extension) + ".sync-conflict-20261028-135632-LOQO3HP" + Extension
	_ = os.WriteFile(conflict, data, 0o644)
	if _, err := Read(conflict); err == nil {
		t.Error("accepted a Syncthing conflict copy")
	}

	// Same manifest, different handoff: the hash must not match.
	tampered := rebuild(t, data, func(name string, body []byte) []byte {
		if name == "handoff.md" {
			return []byte("# Evil instructions")
		}
		return body
	})
	_ = os.WriteFile(path, tampered, 0o644)
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Errorf("accepted a tampered handoff: %v", err)
	}
}

func TestReadRejectsUnsafeEntries(t *testing.T) {
	dir := t.TempDir()
	b := sample()
	path, _ := Write(b, dir, filepath.Join(dir, ".staging"))
	data, _ := os.ReadFile(path)
	evil := rebuild(t, data, nil, "../../.ssh/authorized_keys")
	_ = os.WriteFile(path, evil, 0o644)
	if _, err := Read(path); err == nil {
		t.Error("accepted a bundle with a path outside the bundle")
	}
}

// rebuild unpacks a bundle, optionally changes file bodies, adds extra entries, and packs it again.
func rebuild(t *testing.T, data []byte, change func(string, []byte) []byte, extra ...string) []byte {
	t.Helper()
	dec, _ := zstd.NewReader(nil)
	raw, err := dec.DecodeAll(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(tr)
		b := body.Bytes()
		if change != nil {
			b = change(h.Name, b)
		}
		h.Size = int64(len(b))
		_ = tw.WriteHeader(h)
		_, _ = tw.Write(b)
	}
	for _, name := range extra {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("x"))
	}
	_ = tw.Close()
	enc, _ := zstd.NewWriter(nil)
	return enc.EncodeAll(out.Bytes(), nil)
}
