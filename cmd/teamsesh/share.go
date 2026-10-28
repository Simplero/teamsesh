package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Simplero/teamsesh/internal/bundle"
	"github.com/Simplero/teamsesh/internal/config"
	"github.com/Simplero/teamsesh/internal/daemon"
	"github.com/Simplero/teamsesh/internal/secrets"
	"github.com/Simplero/teamsesh/internal/sessions"
	"github.com/Simplero/teamsesh/internal/sidecar"
)

func cmdList(args []string) error {
	fs := newFlags("ls", "ls [--team | --mine] [-n count]")
	team := fs.Bool("team", false, "sessions your teammates shared")
	mine := fs.Bool("mine", false, "your shares, and who has them")
	n := fs.Int("n", 20, "how many local sessions to list")
	parseInterleaved(fs, args)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	switch {
	case *team:
		shares, err := teamShares()
		if err != nil {
			return err
		}
		if len(shares) == 0 {
			fmt.Println("Nobody on your team has shared a session yet.")
			return nil
		}
		fmt.Fprintln(w, "SHARE\tFROM\tTOOL\tSHARED\tTITLE")
		for _, s := range shares {
			m := s.bundle.Manifest
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.ShareID, s.owner, m.Tool, ago(m.CreatedAt), clip(m.Title, 60))
		}
	case *mine:
		state, err := requireState()
		if err != nil {
			return err
		}
		files, _ := filepath.Glob(filepath.Join(config.FolderPath(state.Self.FolderID), "*"+bundle.Extension))
		if len(files) == 0 {
			fmt.Println("You have not shared anything yet.")
			return nil
		}
		fmt.Fprintln(w, "SHARE\tTOOL\tSHARED\tDELIVERED TO\tTITLE")
		for _, f := range files {
			b, err := bundle.Read(f)
			if err != nil {
				continue
			}
			m := b.Manifest
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.ShareID, m.Tool, ago(m.CreatedAt), delivery(state, filepath.Base(f)), clip(m.Title, 50))
		}
	default:
		all := sessions.Find()
		if len(all) == 0 {
			fmt.Println("No Claude Code, Codex or Grok Build sessions found.")
			return nil
		}
		fmt.Fprintln(w, "ID\tTOOL\tUPDATED\tFOLDER\tTITLE")
		for i, s := range all {
			if i >= *n {
				break
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", displayID(s.ID), s.Tool, ago(s.UpdatedAt), shortDir(s.Dir), clip(s.Title, 60))
		}
	}
	return nil
}

func cmdShare(args []string) error {
	fs := newFlags("share", "share <session-id> | --last [--redact | --force] [--yes] [--wait] [--dry-run]")
	last := fs.Bool("last", false, "share the newest session in the current folder")
	redact := fs.Bool("redact", false, "replace likely secrets with [REDACTED] in the shared copy")
	force := fs.Bool("force", false, "share even if likely secrets were found")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	wait := fs.Bool("wait", false, "wait until at least one teammate has the whole share")
	dryRun := fs.Bool("dry-run", false, "show what would be shared, and stop")
	pos := parseInterleaved(fs, args)

	state, err := requireState()
	if *dryRun && err != nil {
		state, err = &daemon.State{Self: daemon.Hello{Login: "you"}}, nil
	}
	if err != nil {
		return err
	}
	all := sessions.Find()
	var s sessions.Session
	switch {
	case *last:
		cwd, _ := os.Getwd()
		var ok bool
		if s, ok = sessions.Latest(all, cwd); !ok {
			return fmt.Errorf("no session found for %s", cwd)
		}
	case len(pos) == 1:
		found, ok, candidates := sessions.Resolve(all, pos[0])
		if !ok {
			if len(candidates) > 1 {
				return fmt.Errorf("%q matches %d sessions; use more of the ID", pos[0], len(candidates))
			}
			return fmt.Errorf("no session with ID %q (see: teamsesh ls)", pos[0])
		}
		s = found
	default:
		fs.Usage()
		os.Exit(2)
	}

	data, err := sessions.ReadFile(s.Path)
	if err != nil {
		return err
	}
	msgs, err := sessions.Messages(s)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return errors.New("that session has no conversation to share")
	}

	m := bundle.Manifest{
		ShareID: bundle.NewShareID(), Owner: state.Self.Login, Machine: state.Self.Host,
		Tool: string(s.Tool), SessionID: s.ID, Title: s.Title, Dir: s.Dir, Branch: s.Branch,
		Turns: len(msgs), CreatedAt: time.Now().UTC(),
	}
	m.GitRemote, m.Commit, m.Dirty = gitInfo(s.Dir)
	b := &bundle.Bundle{Manifest: m, Handoff: []byte(sessions.Handoff(s.Title, state.Self.Login, s, msgs))}
	switch s.Tool {
	case sessions.Claude:
		b.Native, err = sessions.StripClaude(data, s.ID, s.Dir)
	case sessions.Codex:
		b.Native, err = sessions.StripCodex(data, s.ID, s.Dir)
	}
	if err != nil {
		return fmt.Errorf("cannot prepare the session: %w", err)
	}

	// Check everything that leaves the machine, as it will leave.
	hits := secrets.Scan("handoff", string(b.Handoff))
	nativeHits := secrets.Scan("session", string(b.Native))
	titleHits := secrets.Scan("title", b.Manifest.Title)
	if n := len(hits) + len(nativeHits) + len(titleHits); n > 0 {
		fmt.Printf("Found %d likely secret(s):\n", n)
		for _, h := range append(append(titleHits, hits...), nativeHits...) {
			fmt.Printf("  %-8s line %-6d %-22s %s\n", h.File, h.Line, h.Kind, h.Masked)
		}
		switch {
		case *redact:
			b.Handoff = []byte(secrets.Redact(string(b.Handoff), hits))
			if b.Native != nil {
				b.Native = []byte(secrets.Redact(string(b.Native), nativeHits))
			}
			b.Manifest.Title = secrets.Redact(b.Manifest.Title, titleHits)
			fmt.Println("They will be replaced with [REDACTED] in the shared copy. Your own session stays as it is.")
		case *force:
			fmt.Println("Sharing them anyway (--force).")
		case *dryRun:
		default:
			return errors.New("not shared. Use --redact to replace them, or --force to share as is")
		}
	}

	fmt.Printf("\nAbout to share with your team:\n")
	fmt.Printf("  Title:   %s\n  Tool:    %s\n  Folder:  %s\n  Turns:   %d\n", b.Manifest.Title, s.Tool, shortDir(s.Dir), len(msgs))
	if m.GitRemote != "" {
		fmt.Printf("  Repo:    %s (%s)\n", m.GitRemote, short(m.Commit))
	}
	fmt.Printf("  First:   %s\n  Last:    %s\n", clip(firstUser(msgs), 100), clip(msgs[len(msgs)-1].Text, 100))
	fmt.Println("  Only the conversation is shared: not your CLAUDE.md, AGENTS.md, memory, settings or system prompt.")
	fmt.Printf("  Size:    %d KB (handoff %d KB)\n", (len(b.Native)+len(b.Handoff))/1024, len(b.Handoff)/1024)
	if *dryRun {
		return nil
	}
	if !*yes && !confirm("Share it?") {
		return errors.New("not shared")
	}

	path, err := bundle.Write(b, config.FolderPath(state.Self.FolderID), config.StagingDir())
	if err != nil {
		return err
	}
	fmt.Printf("\nShared as %s. A teammate continues it with:\n\n    teamsesh pick %s\n\n", b.Manifest.ShareID, b.Manifest.ShareID)
	if *wait {
		return waitForDelivery(state, filepath.Base(path))
	}
	fmt.Println("Run \"teamsesh ls --mine\" to see who has it. Keep this machine on until someone does.")
	return nil
}

func cmdUnshare(args []string) error {
	fs := newFlags("unshare", "unshare <share-id>")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 || !bundle.ValidShareID(pos[0]) {
		fs.Usage()
		os.Exit(2)
	}
	state, err := requireState()
	if err != nil {
		return err
	}
	path := filepath.Join(config.FolderPath(state.Self.FolderID), pos[0]+bundle.Extension)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("you have no share %s on this machine", pos[0])
		}
		return err
	}
	fmt.Println("Removed. Teammates' copies disappear the next time they sync. Anyone who already picked it up keeps their session.")
	return nil
}

func cmdShow(args []string) error {
	fs := newFlags("show", "show <share-id>")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		fs.Usage()
		os.Exit(2)
	}
	s, err := findShare(pos[0])
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(s.bundle.Handoff)
	return err
}

func cmdStatus(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	state, err := daemon.LoadState()
	if err != nil {
		fmt.Println("teamsesh has not run yet. Start it with: teamsesh service install")
		return nil
	}
	api := sidecar.NewAPI(cfg.APIKey)
	running := api.Running()
	fmt.Printf("You:      %s on %s\n", state.Self.Login, state.Self.Host)
	if running {
		fmt.Println("Service:  running")
	} else {
		fmt.Println("Service:  NOT running (start it with: teamsesh service install, or teamsesh up)")
	}
	fmt.Printf("Shares:   %s\n", config.FolderPath(state.Self.FolderID))
	connected := map[string]bool{}
	if running {
		connected, _ = api.Connected()
	}
	mark := func(p daemon.Peer) string {
		if connected[p.DeviceID] {
			return p.Host + " (connected)"
		}
		return p.Host + " (not connected)"
	}
	var mine []string
	for _, p := range state.Peers {
		if p.Login == state.Self.Login {
			mine = append(mine, mark(p))
		}
	}
	if len(mine) > 0 {
		fmt.Printf("Your other devices: %s\n", strings.Join(mine, ", "))
	}
	if len(cfg.Team) == 0 {
		fmt.Println("Team:     empty (add people with: teamsesh team add <login>)")
		return nil
	}
	fmt.Println("Team:")
	for _, login := range cfg.Team {
		var devices []string
		for _, p := range state.Peers {
			if p.Login != login {
				continue
			}
			devices = append(devices, mark(p))
		}
		if len(devices) == 0 {
			devices = []string{"no devices found yet: they need teamsesh running, and you on their team list"}
		}
		fmt.Printf("  %-28s %s\n", login, strings.Join(devices, ", "))
	}
	return nil
}

type teamShare struct {
	owner  string
	path   string
	bundle *bundle.Bundle
}

// teamShares lists valid shares in teammates' folders. A share is listed only when its
// manifest owner matches the folder's owner, so nobody can post in someone else's name.
func teamShares() ([]teamShare, error) {
	state, err := requireState()
	if err != nil {
		return nil, err
	}
	var out []teamShare
	for _, p := range state.Peers {
		files, _ := filepath.Glob(filepath.Join(config.FolderPath(p.FolderID), "*"+bundle.Extension))
		for _, f := range files {
			b, err := bundle.Read(f)
			if err != nil || !strings.EqualFold(b.Manifest.Owner, p.Login) {
				continue
			}
			out = append(out, teamShare{owner: p.Login, path: f, bundle: b})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].bundle.Manifest.CreatedAt.After(out[j].bundle.Manifest.CreatedAt)
	})
	return out, nil
}

func findShare(id string) (*teamShare, error) {
	if !bundle.ValidShareID(id) {
		return nil, fmt.Errorf("%q is not a share ID (see: teamsesh ls --team)", id)
	}
	shares, err := teamShares()
	if err != nil {
		return nil, err
	}
	for _, s := range shares {
		if s.bundle.Manifest.ShareID == id {
			return &s, nil
		}
	}
	// Your own shares, for testing pickup on your own machine.
	state, _ := daemon.LoadState()
	path := filepath.Join(config.FolderPath(state.Self.FolderID), id+bundle.Extension)
	if b, err := bundle.Read(path); err == nil {
		return &teamShare{owner: state.Self.Login, path: path, bundle: b}, nil
	}
	return nil, fmt.Errorf("share %s is not here (yet). Check: teamsesh ls --team", id)
}

func requireState() (*daemon.State, error) {
	state, err := daemon.LoadState()
	if err != nil {
		return nil, errors.New("teamsesh has not run yet. Start it with: teamsesh service install")
	}
	return state, nil
}

func delivery(state *daemon.State, file string) string {
	cfg, err := config.Load()
	if err != nil {
		return "?"
	}
	have, err := sidecar.NewAPI(cfg.APIKey).HaveFile(state.Self.FolderID, file)
	if err != nil {
		return "unknown (is the service running?)"
	}
	var who []string
	for _, id := range have {
		for _, p := range state.Peers {
			if p.DeviceID == id {
				who = append(who, p.Login+"/"+p.Host)
			}
		}
	}
	if len(who) == 0 {
		return "nobody yet"
	}
	return strings.Join(who, ", ")
}

func waitForDelivery(state *daemon.State, file string) error {
	fmt.Println("Waiting until a teammate has the whole share (Ctrl-C to stop waiting; the share stays)...")
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if d := delivery(state, file); d != "nobody yet" && !strings.HasPrefix(d, "unknown") {
			fmt.Println("Delivered to", d+". You can close this machine now.")
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return errors.New("no teammate has it after 15 minutes. Check: teamsesh status")
}

var credentialsInURL = regexp.MustCompile(`//[^/@]+@`)

// gitInfo returns the origin URL (without credentials), HEAD, and whether there are
// uncommitted changes.
func gitInfo(dir string) (remote, commit string, dirty bool) {
	if dir == "" {
		return
	}
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	remote = credentialsInURL.ReplaceAllString(run("remote", "get-url", "origin"), "//")
	commit = run("rev-parse", "HEAD")
	dirty = run("status", "--porcelain", "--untracked-files=no") != ""
	return
}

// sameRepo compares git remotes across URL styles (ssh, https, .git suffix).
func sameRepo(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".git"))
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u.Hostname() + "/" + strings.TrimPrefix(u.Path, "/")
		}
		if at := strings.Index(s, "@"); at >= 0 {
			s = s[at+1:]
		}
		return strings.Replace(s, ":", "/", 1)
	}
	return a != "" && b != "" && norm(a) == norm(b)
}

func confirm(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func firstUser(msgs []sessions.Message) string {
	for _, m := range msgs {
		if m.Role == "user" {
			return m.Text
		}
	}
	return ""
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func shortDir(dir string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, h) {
		return "~" + dir[len(h):]
	}
	return dir
}

// displayID is short but unique enough: Codex IDs share their first 8 characters (a timestamp).
func displayID(id string) string {
	if len(id) > 13 {
		return id[:13]
	}
	return id
}
