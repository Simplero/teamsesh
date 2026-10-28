// Package config holds TeamSesh settings and folder locations.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	SyncPort  = 22100 // Syncthing, on the Tailscale IP only
	HelloPort = 22101 // TeamSesh peer hello, on the Tailscale IP only
	APIPort   = 28384 // Syncthing REST API, on 127.0.0.1 only
)

type Config struct {
	Version int      `json:"version"`
	Team    []string `json:"team"`    // Tailscale login names, besides your own
	APIKey  string   `json:"api_key"` // for the sidecar's REST API
}

// Dir holds config, the Syncthing sidecar's home, and logs.
func Dir() string {
	if dir := os.Getenv("TEAMSESH_HOME"); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = filepath.Join(home(), ".config")
	}
	return filepath.Join(base, "teamsesh")
}

// DataDir holds the shared folders. It is visible on purpose: people should see what syncs.
func DataDir() string {
	if dir := os.Getenv("TEAMSESH_DATA"); dir != "" {
		return dir
	}
	return filepath.Join(home(), "TeamSesh")
}

func TeamDir() string       { return filepath.Join(DataDir(), "team") }
func StagingDir() string    { return filepath.Join(DataDir(), ".staging") }
func HandoffDir() string    { return filepath.Join(DataDir(), "handoffs") }
func SyncthingHome() string { return filepath.Join(Dir(), "syncthing") }
func LogDir() string        { return filepath.Join(Dir(), "logs") }
func path() string          { return filepath.Join(Dir(), "config.json") }

// FolderID is the Syncthing folder for one owner's device. It is opaque on purpose: no login
// names in paths.
func FolderID(login, deviceID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(login) + "\n" + deviceID))
	return "ts-" + hex.EncodeToString(sum[:])[:16]
}

func FolderPath(folderID string) string { return filepath.Join(TeamDir(), folderID) }

// Load reads the config, creating it on first use.
func Load() (*Config, error) {
	data, err := os.ReadFile(path())
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 24)
		_, _ = rand.Read(key)
		c := &Config{Version: 1, APIKey: hex.EncodeToString(key)}
		return c, c.Save()
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path())
}

// IsMember reports whether login is on the team. You are always on your own team.
func (c *Config) IsMember(login, self string) bool {
	login = strings.ToLower(login)
	return login == strings.ToLower(self) || slices.Contains(c.Team, login)
}

func (c *Config) Add(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" || slices.Contains(c.Team, login) {
		return false
	}
	c.Team = append(c.Team, login)
	slices.Sort(c.Team)
	return true
}

func (c *Config) Remove(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	i := slices.Index(c.Team, login)
	if i < 0 {
		return false
	}
	c.Team = slices.Delete(c.Team, i, i+1)
	return true
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}
