// Package sidecar runs TeamSesh's own Syncthing instance and talks to its REST API.
//
// It never touches a Syncthing the person already runs: it has its own home folder, binds
// sync only to the Tailscale IP, binds the API only to 127.0.0.1, and turns off discovery,
// relays, NAT traversal, usage reports and auto-upgrade.
package sidecar

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Simplero/teamsesh/internal/config"
)

// SyncthingVersion is the pinned release TeamSesh downloads when none is installed.
const SyncthingVersion = "v2.1.6"

// Binary finds Syncthing: TeamSesh's own download first, then PATH.
func Binary() (string, error) {
	own := ownBinary()
	if _, err := os.Stat(own); err == nil {
		return own, nil
	}
	if p, err := exec.LookPath("syncthing"); err == nil {
		return p, nil
	}
	return "", errors.New("syncthing not found")
}

func ownBinary() string {
	name := "syncthing"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(config.Dir(), "bin", name)
}

// Download fetches the pinned Syncthing release and checks it against the release's
// published SHA-256 list.
func Download() (string, error) {
	osName := map[string]string{"darwin": "macos", "linux": "linux", "windows": "windows"}[runtime.GOOS]
	if osName == "" {
		return "", fmt.Errorf("unsupported system %s", runtime.GOOS)
	}
	arch := runtime.GOARCH
	ext := ".tar.gz"
	if osName != "linux" {
		ext = ".zip"
	}
	if osName == "macos" {
		arch = "universal"
	}
	base := fmt.Sprintf("syncthing-%s-%s-%s", osName, arch, SyncthingVersion)
	release := "https://github.com/syncthing/syncthing/releases/download/" + SyncthingVersion + "/"

	sums, err := fetch(release + "sha256sum.txt.asc")
	if err != nil {
		return "", err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == base+ext {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("no checksum for %s", base+ext)
	}
	archive, err := fetch(release + base + ext)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return "", errors.New("Syncthing download does not match its checksum")
	}

	binName := "syncthing"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	var bin []byte
	if ext == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return "", err
		}
		for _, f := range zr.File {
			if f.Name == base+"/"+binName {
				rc, err := f.Open()
				if err != nil {
					return "", err
				}
				bin, err = io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return "", err
				}
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return "", err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if h.Name == base+"/"+binName {
				if bin, err = io.ReadAll(tr); err != nil {
					return "", err
				}
			}
		}
	}
	if bin == nil {
		return "", errors.New("syncthing binary not found in the download")
	}
	dst := ownBinary()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst+".tmp", bin, 0o755); err != nil {
		return "", err
	}
	return dst, os.Rename(dst+".tmp", dst)
}

func fetch(u string) ([]byte, error) {
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

var deviceIDPattern = regexp.MustCompile(`[A-Z2-7]{7}(-[A-Z2-7]{7}){7}`)

// Prepare creates the sidecar's home on first use and returns this device's Syncthing ID.
func Prepare(bin, tsIP string) (string, error) {
	home := config.SyncthingHome()
	cfgPath := filepath.Join(home, "config.xml")
	if _, err := os.Stat(cfgPath); err != nil {
		cmd := exec.Command(bin, "generate", "--home="+home, "--no-port-probing")
		cmd.Env = append(os.Environ(), "STNODEFAULTFOLDER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("syncthing generate: %v: %s", err, out)
		}
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			return "", err
		}
		cfg := string(data)
		// Never let a default folder (for example ~/Sync) into TeamSesh's instance.
		cfg = regexp.MustCompile(`(?s)\s*<folder [^>]*>.*?</folder>`).ReplaceAllString(cfg, "")
		cfg = regexp.MustCompile(`<listenAddress>[^<]*</listenAddress>`).ReplaceAllString(cfg, "")
		cfg = strings.Replace(cfg, "<options>", fmt.Sprintf("<options>\n        <listenAddress>tcp://%s:%d</listenAddress>", tsIP, config.SyncPort), 1)
		for tag, value := range map[string]string{
			"globalAnnounceEnabled": "false", "localAnnounceEnabled": "false", "relaysEnabled": "false",
			"natEnabled": "false", "autoUpgradeIntervalH": "0", "urAccepted": "-1", "crashReportingEnabled": "false",
			"startBrowser": "false",
		} {
			cfg = regexp.MustCompile(`<`+tag+`>[^<]*</`+tag+`>`).ReplaceAllString(cfg, "<"+tag+">"+value+"</"+tag+">")
		}
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
			return "", err
		}
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", err
	}
	// The first <device> in a generated config is this device.
	m := regexp.MustCompile(`<device id="([A-Z2-7-]+)"`).FindSubmatch(data)
	if m == nil || !deviceIDPattern.Match(m[1]) {
		return "", errors.New("cannot read this device's Syncthing ID")
	}
	return string(m[1]), nil
}

// Start runs the sidecar. The caller owns the process.
func Start(bin, apiKey string) (*exec.Cmd, error) {
	if err := os.MkdirAll(config.LogDir(), 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(config.LogDir(), "syncthing.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "serve", "--home="+config.SyncthingHome(), "--no-browser", "--no-upgrade",
		fmt.Sprintf("--gui-address=127.0.0.1:%d", config.APIPort))
	cmd.Env = append(os.Environ(), "STGUIAPIKEY="+apiKey, "STNODEFAULTFOLDER=1", "STNOUPGRADE=1")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	return cmd, nil
}

// API is a small client for the sidecar's REST API.
type API struct {
	Key  string
	http http.Client
}

func NewAPI(key string) *API { return &API{Key: key, http: http.Client{Timeout: 15 * time.Second}} }

func (a *API) do(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", config.APIPort, path), r)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", a.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Ready waits until the API answers.
func (a *API) Ready(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := a.do("GET", "/rest/system/ping", nil, nil); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("sidecar did not start: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Running reports whether a sidecar answers on the API port.
func (a *API) Running() bool { return a.do("GET", "/rest/system/ping", nil, nil) == nil }

// SetListen keeps the sync listener on the current Tailscale IP, and the privacy options off.
func (a *API) SetListen(tsIP string) error {
	return a.do("PATCH", "/rest/config/options", map[string]any{
		"listenAddresses":       []string{fmt.Sprintf("tcp://%s:%d", tsIP, config.SyncPort)},
		"globalAnnounceEnabled": false, "localAnnounceEnabled": false, "relaysEnabled": false,
		"natEnabled": false, "autoUpgradeIntervalH": 0, "urAccepted": -1, "crashReportingEnabled": false,
	}, nil)
}

type Device struct {
	DeviceID  string   `json:"deviceID"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
}

func (a *API) Devices() ([]Device, error) {
	var d []Device
	return d, a.do("GET", "/rest/config/devices", nil, &d)
}

func (a *API) PutDevice(d Device) error {
	return a.do("PUT", "/rest/config/devices/"+d.DeviceID, map[string]any{
		"deviceID": d.DeviceID, "name": d.Name, "addresses": d.Addresses,
		"compression": "metadata", "introducer": false, "autoAcceptFolders": false,
	}, nil)
}

func (a *API) DeleteDevice(id string) error {
	return a.do("DELETE", "/rest/config/devices/"+id, nil, nil)
}

type Folder struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Type    string `json:"type"` // "sendonly" for your own, "receiveonly" for everyone else's
	Devices []struct {
		DeviceID string `json:"deviceID"`
	} `json:"devices"`
}

func (a *API) Folders() ([]Folder, error) {
	var f []Folder
	return f, a.do("GET", "/rest/config/folders", nil, &f)
}

func (a *API) PutFolder(id, label, path, kind string, devices []string) error {
	var devs []map[string]string
	for _, d := range devices {
		devs = append(devs, map[string]string{"deviceID": d})
	}
	return a.do("PUT", "/rest/config/folders/"+id, map[string]any{
		"id": id, "label": label, "path": path, "type": kind, "devices": devs,
		"rescanIntervalS": 60, "fsWatcherEnabled": true, "fsWatcherDelayS": 1,
		"ignorePerms": true, "autoNormalize": true, "maxConflicts": 0,
	}, nil)
}

func (a *API) DeleteFolder(id string) error {
	return a.do("DELETE", "/rest/config/folders/"+id, nil, nil)
}

type FolderStatus struct {
	State                   string `json:"state"`
	ReceiveOnlyChangedFiles int    `json:"receiveOnlyChangedFiles"`
	NeedFiles               int    `json:"needFiles"`
}

func (a *API) FolderStatus(id string) (FolderStatus, error) {
	var s FolderStatus
	return s, a.do("GET", "/rest/db/status?folder="+url.QueryEscape(id), nil, &s)
}

// Revert throws away local changes in a receive-only folder, so it matches the owner again.
func (a *API) Revert(id string) error {
	return a.do("POST", "/rest/db/revert?folder="+url.QueryEscape(id), nil, nil)
}

func (a *API) Scan(id string) error {
	return a.do("POST", "/rest/db/scan?folder="+url.QueryEscape(id), nil, nil)
}

// HaveFile lists the devices that have the current version of a file.
func (a *API) HaveFile(folder, file string) ([]string, error) {
	var r struct {
		Availability []struct {
			ID string `json:"id"`
		} `json:"availability"`
	}
	if err := a.do("GET", "/rest/db/file?folder="+url.QueryEscape(folder)+"&file="+url.QueryEscape(file), nil, &r); err != nil {
		return nil, err
	}
	var ids []string
	for _, d := range r.Availability {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// Connected lists device IDs with a live connection.
func (a *API) Connected() (map[string]bool, error) {
	var r struct {
		Connections map[string]struct {
			Connected bool `json:"connected"`
		} `json:"connections"`
	}
	if err := a.do("GET", "/rest/system/connections", nil, &r); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, c := range r.Connections {
		out[id] = c.Connected
	}
	return out, nil
}

// LastLogLines returns the end of the sidecar log, for error messages.
func LastLogLines(n int) string {
	f, err := os.Open(filepath.Join(config.LogDir(), "syncthing.log"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}
