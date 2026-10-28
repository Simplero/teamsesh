// Package bundle reads and writes a share: one immutable tar.zst file with a manifest,
// a handoff, and an optional stripped native session.
package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	FormatVersion = 1
	Extension     = ".teamsesh"

	maxBundleBytes   = 64 << 20  // compressed
	maxUnpackedBytes = 512 << 20 // all files together
)

const (
	fileManifest = "manifest.json"
	fileHandoff  = "handoff.md"
	fileNative   = "native/session.jsonl"
)

type Manifest struct {
	Format    int       `json:"format"`
	ShareID   string    `json:"share_id"`
	Owner     string    `json:"owner"`
	Machine   string    `json:"machine"`
	Tool      string    `json:"tool"`
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	Dir       string    `json:"dir,omitempty"`
	GitRemote string    `json:"git_remote,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Dirty     bool      `json:"dirty,omitempty"`
	Turns     int       `json:"turns"`
	CreatedAt time.Time `json:"created_at"`
	// SHA-256 of every other file in the bundle.
	Files map[string]string `json:"files"`
}

type Bundle struct {
	Manifest Manifest
	Handoff  []byte
	Native   []byte // nil when the tool has no native import
}

var shareIDPattern = regexp.MustCompile(`^[a-z2-7]{10}$`)

// NewShareID returns a random ID like "k3x7q2mzpa".
func NewShareID() string {
	b := make([]byte, 7)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:10]
}

func ValidShareID(id string) bool { return shareIDPattern.MatchString(id) }

// Write saves the bundle atomically as <dir>/<share-id>.teamsesh. It writes to staging first
// (which must be on the same disk), so a syncing folder never sees a half-written file.
func Write(b *Bundle, dir, staging string) (string, error) {
	if !ValidShareID(b.Manifest.ShareID) {
		return "", errors.New("bad share ID")
	}
	b.Manifest.Format = FormatVersion
	b.Manifest.Files = map[string]string{fileHandoff: digest(b.Handoff)}
	if b.Native != nil {
		b.Manifest.Files[fileNative] = digest(b.Native)
	}
	manifest, err := json.MarshalIndent(b.Manifest, "", "  ")
	if err != nil {
		return "", err
	}

	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	add := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: b.Manifest.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := add(fileManifest, manifest); err != nil {
		return "", err
	}
	if err := add(fileHandoff, b.Handoff); err != nil {
		return "", err
	}
	if b.Native != nil {
		if err := add(fileNative, b.Native); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return "", err
	}
	compressed := enc.EncodeAll(raw.Bytes(), nil)
	if len(compressed) > maxBundleBytes {
		return "", fmt.Errorf("the share is too big (%d MB, the limit is %d MB)", len(compressed)>>20, maxBundleBytes>>20)
	}

	if err := os.MkdirAll(staging, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(staging, "share-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(compressed); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	final := filepath.Join(dir, b.Manifest.ShareID+Extension)
	return final, os.Rename(tmp.Name(), final)
}

// Read opens and checks a bundle. It refuses anything unexpected: unknown format, wrong
// file name, extra or unsafe entries, oversized content, or a hash that does not match.
func Read(path string) (*Bundle, error) {
	name := filepath.Base(path)
	if !strings.HasSuffix(name, Extension) || !ValidShareID(strings.TrimSuffix(name, Extension)) {
		return nil, errors.New("not a share file")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBundleBytes {
		return nil, errors.New("not a regular share file, or too big")
	}
	compressed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxUnpackedBytes))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot unpack: %w", err)
	}

	files := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || (h.Name != fileManifest && h.Name != fileHandoff && h.Name != fileNative) {
			return nil, fmt.Errorf("unexpected entry %q", h.Name)
		}
		if _, dup := files[h.Name]; dup {
			return nil, fmt.Errorf("duplicate entry %q", h.Name)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxUnpackedBytes))
		if err != nil {
			return nil, err
		}
		files[h.Name] = data
	}

	var b Bundle
	if err := json.Unmarshal(files[fileManifest], &b.Manifest); err != nil {
		return nil, errors.New("missing or broken manifest")
	}
	m := b.Manifest
	if m.Format != FormatVersion {
		return nil, fmt.Errorf("share format %d needs a newer teamsesh", m.Format)
	}
	if m.ShareID+Extension != name {
		return nil, errors.New("file name does not match the share ID")
	}
	for entry := range files {
		if entry == fileManifest {
			continue
		}
		if want, ok := m.Files[entry]; !ok || want != digest(files[entry]) {
			return nil, fmt.Errorf("%s does not match its hash", entry)
		}
	}
	for entry := range m.Files {
		if _, ok := files[entry]; !ok {
			return nil, fmt.Errorf("%s is missing", entry)
		}
	}
	b.Handoff = files[fileHandoff]
	b.Native = files[fileNative]
	return &b, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
