// Package tailnet asks the local Tailscale client who is who.
package tailnet

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type Peer struct {
	Host   string
	IP     string // first IPv4 Tailscale address
	Login  string // owner's login name; empty for tagged devices
	Online bool
	Shared bool // shared in from another tailnet
}

type Status struct {
	Self  Peer
	Peers []Peer
}

// Binary finds the Tailscale CLI.
func Binary() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	candidates := map[string][]string{
		"darwin":  {"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale"},
		"windows": {`C:\Program Files\Tailscale\tailscale.exe`},
	}[runtime.GOOS]
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Tailscale is not installed. Install it from https://tailscale.com/download and log in")
}

type rawNode struct {
	HostName     string   `json:"HostName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	UserID       int64    `json:"UserID"`
	Online       bool     `json:"Online"`
	ShareeNode   bool     `json:"ShareeNode"`
	Tags         []string `json:"Tags"`
}

// Current reads `tailscale status --json`.
func Current() (*Status, error) {
	bin, err := Binary()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(bin, "status", "--json").Output()
	if err != nil {
		return nil, errors.New("Tailscale is not running or not logged in")
	}
	var raw struct {
		BackendState string             `json:"BackendState"`
		Self         rawNode            `json:"Self"`
		Peer         map[string]rawNode `json:"Peer"`
		User         map[string]struct {
			LoginName string `json:"LoginName"`
		} `json:"User"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	if raw.BackendState != "Running" {
		return nil, errors.New("Tailscale is not connected (" + raw.BackendState + ")")
	}
	login := func(n rawNode) string {
		if len(n.Tags) > 0 {
			return "" // tagged devices have no person behind them
		}
		for id, u := range raw.User {
			if id == itoa(n.UserID) {
				return strings.ToLower(u.LoginName)
			}
		}
		return ""
	}
	convert := func(n rawNode) Peer {
		return Peer{Host: n.HostName, IP: ipv4(n.TailscaleIPs), Login: login(n), Online: n.Online, Shared: n.ShareeNode}
	}
	s := &Status{Self: convert(raw.Self)}
	s.Self.Online = true
	for _, n := range raw.Peer {
		s.Peers = append(s.Peers, convert(n))
	}
	return s, nil
}

// WhoIs asks Tailscale which login owns the device at ip. It is the check that the hello
// server uses on every incoming connection.
func WhoIs(ip string) (string, error) {
	bin, err := Binary()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(bin, "whois", "--json", ip).Output()
	if err != nil {
		return "", errors.New("unknown device")
	}
	var w struct {
		Node struct {
			Tags []string `json:"Tags"`
		} `json:"Node"`
		UserProfile struct {
			LoginName string `json:"LoginName"`
		} `json:"UserProfile"`
	}
	if err := json.Unmarshal(out, &w); err != nil {
		return "", err
	}
	if len(w.Node.Tags) > 0 || w.UserProfile.LoginName == "" {
		return "", errors.New("tagged or anonymous device")
	}
	return strings.ToLower(w.UserProfile.LoginName), nil
}

func ipv4(ips []string) string {
	for _, ip := range ips {
		if strings.Count(ip, ".") == 3 {
			return ip
		}
	}
	return ""
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
