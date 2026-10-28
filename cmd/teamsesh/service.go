package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Simplero/teamsesh/internal/config"
)

const launchdLabel = "com.simplero.teamsesh"

func cmdService(args []string) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall") {
		return errors.New("usage: teamsesh service install|uninstall")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	install := args[0] == "install"
	switch runtime.GOOS {
	case "darwin":
		return launchd(exe, install)
	case "linux":
		return systemd(exe, install)
	default:
		return fmt.Errorf("the background service is not supported on %s yet; run \"teamsesh up\" in a terminal", runtime.GOOS)
	}
}

func launchd(exe string, install bool) error {
	home, _ := os.UserHomeDir()
	plist := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	target := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", target+"/"+launchdLabel).Run()
	if !install {
		_ = os.Remove(plist)
		fmt.Println("Stopped and removed the teamsesh service.")
		return nil
	}
	logFile := filepath.Join(config.LogDir(), "teamsesh.log")
	if err := os.MkdirAll(config.LogDir(), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>%s</string>
    <key>ProgramArguments</key><array><string>%s</string><string>up</string></array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ThrottleInterval</key><integer>30</integer>
    <key>ProcessType</key><string>Background</string>
    <key>EnvironmentVariables</key><dict>
        <key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchdLabel, xmlEscape(exe), xmlEscape(logFile), xmlEscape(logFile))
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(content), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "bootstrap", target, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	fmt.Printf("teamsesh now runs in the background and starts at login.\nLog: %s\n", logFile)
	return nil
}

func systemd(exe string, install bool) error {
	home, _ := os.UserHomeDir()
	unit := filepath.Join(home, ".config", "systemd", "user", "teamsesh.service")
	if !install {
		_ = exec.Command("systemctl", "--user", "disable", "--now", "teamsesh.service").Run()
		_ = os.Remove(unit)
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Println("Stopped and removed the teamsesh service.")
		return nil
	}
	content := fmt.Sprintf(`[Unit]
Description=TeamSesh: share AI coding sessions with your team
After=network-online.target

[Service]
ExecStart=%s up
Restart=always
RestartSec=30

[Install]
WantedBy=default.target
`, exe)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unit, []byte(content), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "teamsesh.service"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	fmt.Println("teamsesh now runs in the background (systemd user service).")
	fmt.Println("To keep it running while you are logged out: sudo loginctl enable-linger $USER")
	fmt.Println("Log: journalctl --user -u teamsesh")
	return nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
