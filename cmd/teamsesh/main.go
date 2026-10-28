// Command teamsesh shares AI coding sessions with your team over Tailscale.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Simplero/teamsesh/internal/config"
	"github.com/Simplero/teamsesh/internal/daemon"
)

var version = "dev"

const usage = `teamsesh shares AI coding sessions with your team over Tailscale.

Sharing:
  teamsesh ls                      your recent sessions (Claude Code, Codex, Grok Build)
  teamsesh share <id> | --last     share a session with your team
  teamsesh ls --team               sessions your teammates shared
  teamsesh pick <share-id>         continue a teammate's session here
  teamsesh show <share-id>         print a share's handoff
  teamsesh ls --mine               your shares, and who has them
  teamsesh unshare <share-id>      stop sharing (best effort: copies already picked up stay)

Team:
  teamsesh team                    list your team
  teamsesh team add <login>        add a person by Tailscale login (see: tailscale status)
  teamsesh team remove <login>

Running:
  teamsesh up                      run in the foreground (the service does this for you)
  teamsesh service install         start teamsesh at login, in the background
  teamsesh service uninstall
  teamsesh status                  connection and sync state
  teamsesh version

Run "teamsesh <command> -h" for a command's options.
`

func main() {
	daemon.Version = version
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "ls", "list":
		err = cmdList(args)
	case "share":
		err = cmdShare(args)
	case "unshare":
		err = cmdUnshare(args)
	case "pick":
		err = cmdPick(args)
	case "show":
		err = cmdShow(args)
	case "team":
		err = cmdTeam(args)
	case "status":
		err = cmdStatus(args)
	case "up":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = daemon.Run(ctx)
	case "service":
		err = cmdService(args)
	case "version", "--version", "-v":
		fmt.Println("teamsesh", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "teamsesh:", err)
		os.Exit(1)
	}
}

func cmdTeam(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "ls" {
		if len(cfg.Team) == 0 {
			fmt.Println("Your team is empty. Add people with: teamsesh team add <tailscale-login>")
			fmt.Println("Each person must add you too. Logins look like alice@example.com or alice@github.")
			return nil
		}
		for _, login := range cfg.Team {
			fmt.Println(login)
		}
		return nil
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: teamsesh team add|remove <login>")
	}
	login := strings.ToLower(args[1])
	switch args[0] {
	case "add":
		if !cfg.Add(login) {
			fmt.Println(login, "is already on your team")
			return nil
		}
		fmt.Printf("Added %s. They must also run: teamsesh team add <your login>\n", login)
	case "remove", "rm":
		if !cfg.Remove(login) {
			return fmt.Errorf("%s is not on your team", login)
		}
		fmt.Printf("Removed %s. Their devices stop syncing within a minute.\n", login)
	default:
		return fmt.Errorf("usage: teamsesh team add|remove <login>")
	}
	return cfg.Save()
}

func newFlags(name, use string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: teamsesh %s\n", use)
		fs.PrintDefaults()
	}
	return fs
}

// parseInterleaved lets flags come before or after positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		_ = fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return positional
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
