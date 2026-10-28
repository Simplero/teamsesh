// Package daemon is `teamsesh up`: it keeps the sidecar running and the team paired.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Simplero/teamsesh/internal/bundle"
	"github.com/Simplero/teamsesh/internal/config"
	"github.com/Simplero/teamsesh/internal/sidecar"
	"github.com/Simplero/teamsesh/internal/tailnet"
)

// Version is set by main.
var Version = "dev"

const (
	refreshEvery = 30 * time.Second
	keepShares   = 90 * 24 * time.Hour
)

// Hello is what one TeamSesh tells another over the tailnet.
type Hello struct {
	Login    string `json:"login"`
	DeviceID string `json:"device_id"`
	FolderID string `json:"folder_id"`
	Host     string `json:"host"`
	Version  string `json:"version"`
}

// Peer is a paired team device. The daemon records them in peers.json for `status` and `ls`.
type Peer struct {
	Login    string    `json:"login"`
	Host     string    `json:"host"`
	IP       string    `json:"ip"`
	DeviceID string    `json:"device_id"`
	FolderID string    `json:"folder_id"`
	SeenAt   time.Time `json:"seen_at"`
}

type State struct {
	Self  Hello  `json:"self"`
	Peers []Peer `json:"peers"`
}

func statePath() string { return filepath.Join(config.Dir(), "peers.json") }

// LoadState reads what the daemon last saw.
func LoadState() (*State, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return nil, err
	}
	var s State
	return &s, json.Unmarshal(data, &s)
}

func saveState(s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath())
}

// Run starts the sidecar and the hello server, then keeps everything in shape until ctx ends.
func Run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()

	ts := waitForTailscale(ctx)
	if ts == nil {
		return ctx.Err()
	}

	bin, err := sidecar.Binary()
	if err != nil {
		log.Printf("downloading Syncthing %s", sidecar.SyncthingVersion)
		if bin, err = sidecar.Download(); err != nil {
			return fmt.Errorf("cannot get Syncthing: %w", err)
		}
	}
	deviceID, err := sidecar.Prepare(bin, ts.Self.IP)
	if err != nil {
		return err
	}
	self := Hello{Login: ts.Self.Login, DeviceID: deviceID, FolderID: config.FolderID(ts.Self.Login, deviceID), Host: ts.Self.Host, Version: Version}
	for _, dir := range []string{config.FolderPath(self.FolderID), config.StagingDir(), config.HandoffDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	api := sidecar.NewAPI(cfg.APIKey)
	var proc *exec.Cmd
	startSidecar := func() error {
		if api.Running() {
			return nil
		}
		p, err := sidecar.Start(bin, cfg.APIKey)
		if err != nil {
			return err
		}
		proc = p
		go func() { _ = p.Wait() }()
		if err := api.Ready(30 * time.Second); err != nil {
			return fmt.Errorf("%w\n%s", err, sidecar.LastLogLines(15))
		}
		return nil
	}
	if err := startSidecar(); err != nil {
		return err
	}
	defer func() {
		if proc != nil && proc.Process != nil {
			_ = proc.Process.Signal(os.Interrupt)
		}
	}()

	d := &daemon{cfg: cfg, api: api, self: self}
	srv := d.helloServer(ts.Self.IP)
	defer srv.Close()

	log.Printf("teamsesh %s up as %s (%s), sharing folder %s", Version, self.Login, self.Host, config.FolderPath(self.FolderID))
	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()
	for {
		if err := startSidecar(); err != nil {
			log.Printf("sidecar: %v", err)
		} else if err := d.refresh(); err != nil {
			log.Printf("refresh: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type daemon struct {
	cfg  *config.Config
	api  *sidecar.API
	self Hello

	mu       sync.Mutex
	listenIP string
}

// refresh re-reads the team list and Tailscale, pairs with team devices, removes everyone
// else, and repairs receive-only folders.
func (d *daemon) refresh() error {
	if cfg, err := config.Load(); err == nil {
		d.cfg = cfg
	}
	ts, err := tailnet.Current()
	if err != nil {
		return err
	}
	if ts.Self.Login != d.self.Login {
		return fmt.Errorf("Tailscale is now logged in as %s, not %s; restart teamsesh", ts.Self.Login, d.self.Login)
	}
	d.mu.Lock()
	ipChanged := d.listenIP != ts.Self.IP
	d.listenIP = ts.Self.IP
	d.mu.Unlock()
	if ipChanged {
		if err := d.api.SetListen(ts.Self.IP); err != nil {
			return err
		}
	}

	old, _ := LoadState()
	known := map[string]Peer{}
	if old != nil {
		for _, p := range old.Peers {
			known[p.DeviceID] = p
		}
	}

	// Ask every online team device who it is.
	var peers []Peer
	for _, p := range ts.Peers {
		if !p.Online || p.Shared || p.Login == "" || p.IP == "" || !d.cfg.IsMember(p.Login, d.self.Login) {
			continue
		}
		h, err := askHello(p.IP)
		if err != nil {
			continue // no TeamSesh there, or it does not list us
		}
		// The device must be who Tailscale says owns it.
		if h.Login != p.Login || h.FolderID != config.FolderID(h.Login, h.DeviceID) {
			log.Printf("ignoring %s: it claims %s, Tailscale says %s", p.Host, h.Login, p.Login)
			continue
		}
		peers = append(peers, Peer{Login: h.Login, Host: h.Host, IP: p.IP, DeviceID: h.DeviceID, FolderID: h.FolderID, SeenAt: time.Now()})
		delete(known, h.DeviceID)
	}
	// Keep team devices that are offline right now, so their shares stay listed.
	for _, p := range known {
		if d.cfg.IsMember(p.Login, d.self.Login) {
			peers = append(peers, p)
		}
	}
	slices.SortFunc(peers, func(a, b Peer) int { return strings.Compare(a.Login+a.Host, b.Login+b.Host) })

	if err := d.applyPeers(peers); err != nil {
		return err
	}
	d.cleanUp(peers)
	return saveState(&State{Self: d.self, Peers: peers})
}

// applyPeers makes the sidecar's devices and folders match the team exactly.
// Every team device gets every team folder, so shares spread even when the owner is offline.
func (d *daemon) applyPeers(peers []Peer) error {
	var deviceIDs []string
	for _, p := range peers {
		deviceIDs = append(deviceIDs, p.DeviceID)
		if err := d.api.PutDevice(sidecar.Device{DeviceID: p.DeviceID, Name: p.Login + " on " + p.Host,
			Addresses: []string{fmt.Sprintf("tcp://%s:%d", p.IP, config.SyncPort)}}); err != nil {
			return err
		}
	}
	all := append([]string{d.self.DeviceID}, deviceIDs...)

	if err := d.api.PutFolder(d.self.FolderID, "TeamSesh: "+d.self.Login+" on "+d.self.Host,
		config.FolderPath(d.self.FolderID), "sendonly", all); err != nil {
		return err
	}
	wanted := map[string]bool{d.self.FolderID: true}
	for _, p := range peers {
		wanted[p.FolderID] = true
		path := config.FolderPath(p.FolderID)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if err := d.api.PutFolder(p.FolderID, "TeamSesh: "+p.Login+" on "+p.Host, path, "receiveonly", all); err != nil {
			return err
		}
	}

	// Remove folders and devices that are no longer on the team.
	folders, err := d.api.Folders()
	if err != nil {
		return err
	}
	for _, f := range folders {
		if !wanted[f.ID] {
			_ = d.api.DeleteFolder(f.ID)
		}
	}
	devices, err := d.api.Devices()
	if err != nil {
		return err
	}
	for _, dev := range devices {
		if dev.DeviceID != d.self.DeviceID && !slices.Contains(deviceIDs, dev.DeviceID) {
			_ = d.api.DeleteDevice(dev.DeviceID)
		}
	}
	return nil
}

// cleanUp reverts local edits in receive-only folders (they include Syncthing conflict
// copies) and removes your own shares older than 90 days.
func (d *daemon) cleanUp(peers []Peer) {
	for _, p := range peers {
		if s, err := d.api.FolderStatus(p.FolderID); err == nil && s.ReceiveOnlyChangedFiles > 0 {
			log.Printf("reverting %d local changes in %s's folder", s.ReceiveOnlyChangedFiles, p.Login)
			_ = d.api.Revert(p.FolderID)
		}
	}
	files, _ := filepath.Glob(filepath.Join(config.FolderPath(d.self.FolderID), "*"+bundle.Extension))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && time.Since(info.ModTime()) > keepShares {
			_ = os.Remove(f)
		}
	}
}

// helloServer answers team devices only. Tailscale vouches for the caller's identity.
func (d *daemon) helloServer(ip string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hello", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "bad address", http.StatusBadRequest)
			return
		}
		login, err := tailnet.WhoIs(host)
		if err != nil || !d.cfg.IsMember(login, d.self.Login) {
			http.Error(w, "not on this team", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.self)
	})
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", ip, config.HelloPort), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("hello server: %v", err)
		}
	}()
	return srv
}

func askHello(ip string) (*Hello, error) {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/v1/hello", ip, config.HelloPort))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	var h Hello
	return &h, json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h)
}

func waitForTailscale(ctx context.Context) *tailnet.Status {
	warned := false
	for {
		ts, err := tailnet.Current()
		if err == nil && ts.Self.IP != "" && ts.Self.Login != "" {
			return ts
		}
		if !warned {
			log.Printf("waiting for Tailscale: %v", err)
			warned = true
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
}

// lock makes sure only one daemon runs per user.
func lock() (func(), error) {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(config.Dir(), "daemon.pid")
	if data, err := os.ReadFile(path); err == nil {
		var pid int
		if _, err := fmt.Sscan(string(data), &pid); err == nil && pid != os.Getpid() && processAlive(pid) {
			return nil, fmt.Errorf("teamsesh is already running (pid %d)", pid)
		}
	}
	if err := os.WriteFile(path, []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}
