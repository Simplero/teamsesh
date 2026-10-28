package sessions

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CodexHome is $CODEX_HOME, or ~/.codex.
func CodexHome() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home(), ".codex")
}

func findCodex() []Session {
	var out []Session
	root := filepath.Join(CodexHome(), "sessions")
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real // WalkDir does not follow a symlinked root
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !(strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.zst")) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 {
			return nil
		}
		head := readHead(path, 2*1024*1024)
		s := Session{Tool: Codex, Path: path, UpdatedAt: info.ModTime(), Size: info.Size()}
		var firstPrompt string
		eachLine(head, func(o map[string]any) bool {
			p := obj(o, "payload")
			switch str(o, "type") {
			case "session_meta":
				if s.ID == "" {
					s.ID = str(p, "id")
					s.Dir = str(p, "cwd")
					s.Branch = str(obj(p, "git"), "branch")
				}
			case "response_item":
				if firstPrompt == "" && str(p, "type") == "message" && str(p, "role") == "user" {
					if text := textOf(p["content"]); !isInjected(text) {
						firstPrompt = text
					}
				}
			case "event_msg":
				if firstPrompt == "" && str(p, "type") == "user_message" && !isInjected(str(p, "message")) {
					firstPrompt = str(p, "message")
				}
			}
			return s.ID == "" || firstPrompt == ""
		})
		if s.ID == "" || firstPrompt == "" {
			return nil
		}
		s.Title = oneLine(firstPrompt, 80)
		out = append(out, s)
		return nil
	})
	return out
}

// StripCodex keeps session_meta (without base instructions and account IDs) and the real
// user and assistant messages. It drops developer messages (permissions, tools), injected
// AGENTS.md text, reasoning (encrypted for the sender's account), and tool calls.
func StripCodex(data []byte, newID, newDir string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	wroteMeta := false
	var err error
	eachLine(data, func(o map[string]any) bool {
		p := obj(o, "payload")
		switch str(o, "type") {
		case "session_meta":
			if wroteMeta {
				return true
			}
			wroteMeta = true
			meta := map[string]any{}
			for _, key := range []string{"timestamp", "originator", "cli_version", "source", "model_provider"} {
				if v, ok := p[key]; ok {
					meta[key] = v
				}
			}
			meta["id"] = newID
			meta["cwd"] = newDir
			if git := obj(p, "git"); git != nil {
				meta["git"] = map[string]any{"branch": git["branch"], "commit_hash": git["commit_hash"]}
			}
			o["payload"] = meta
		case "response_item":
			if str(p, "type") != "message" {
				return true
			}
			role := str(p, "role")
			if role != "user" && role != "assistant" {
				return true
			}
			if role == "user" && isInjected(textOf(p["content"])) {
				return true
			}
			o["payload"] = map[string]any{"type": "message", "role": role, "content": p["content"]}
		default:
			return true
		}
		if err = enc.Encode(map[string]any{"timestamp": o["timestamp"], "type": o["type"], "payload": o["payload"]}); err != nil {
			return false
		}
		return true
	})
	return buf.Bytes(), err
}

// CodexRolloutPath is where an imported Codex session goes.
func CodexRolloutPath(id string, now time.Time) string {
	return filepath.Join(CodexHome(), "sessions", now.Format("2006"), now.Format("01"), now.Format("02"),
		"rollout-"+now.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
}

// CodexMessages reads the conversation as plain text turns.
func CodexMessages(data []byte) []Message {
	var out []Message
	eachLine(data, func(o map[string]any) bool {
		if str(o, "type") != "response_item" {
			return true
		}
		p := obj(o, "payload")
		role := str(p, "role")
		if str(p, "type") != "message" || (role != "user" && role != "assistant") {
			return true
		}
		text := strings.TrimSpace(textOf(p["content"]))
		if role == "user" && isInjected(text) {
			return true
		}
		if text != "" {
			out = append(out, Message{Role: role, Text: text})
		}
		return true
	})
	return out
}
