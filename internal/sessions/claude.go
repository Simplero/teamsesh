package sessions

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ClaudeConfigDirs lists Claude Code config folders: $CLAUDE_CONFIG_DIR, ~/.claude, and ~/.claude-*.
// Folders whose projects/ resolve to the same place count once.
func ClaudeConfigDirs() []string {
	var candidates []string
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		candidates = append(candidates, dir)
	}
	candidates = append(candidates, filepath.Join(home(), ".claude"))
	if extra, _ := filepath.Glob(filepath.Join(home(), ".claude-*")); extra != nil {
		candidates = append(candidates, extra...)
	}
	seen := map[string]bool{}
	var dirs []string
	for _, dir := range candidates {
		projects, err := filepath.EvalSymlinks(filepath.Join(dir, "projects"))
		if err != nil || seen[projects] {
			continue
		}
		seen[projects] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// ClaudeHome is where an imported session goes: $CLAUDE_CONFIG_DIR, or ~/.claude.
func ClaudeHome() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(home(), ".claude")
}

// ClaudeProjectFolder is the folder name Claude Code uses for a working folder.
func ClaudeProjectFolder(dir string) string {
	return regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(dir, "-")
}

func findClaude() []Session {
	var out []Session
	seen := map[string]bool{} // project folders can be symlinks to each other
	for _, dir := range ClaudeConfigDirs() {
		files, _ := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
		for _, path := range files {
			if real, err := filepath.EvalSymlinks(path); err == nil {
				if seen[real] {
					continue
				}
				seen[real] = true
			}
			id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			if !uuidPattern.MatchString(id) {
				continue
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				continue
			}
			s := Session{Tool: Claude, ID: id, Path: path, UpdatedAt: info.ModTime(), Size: info.Size()}
			var firstPrompt string
			eachLine(readHead(path, 512*1024), func(o map[string]any) bool {
				if s.Dir == "" {
					s.Dir = str(o, "cwd")
					s.Branch = str(o, "gitBranch")
				}
				if firstPrompt == "" && str(o, "type") == "user" && o["isSidechain"] != true && o["isMeta"] != true {
					if text := textOf(obj(o, "message")["content"]); !isInjected(text) {
						firstPrompt = text
					}
				}
				return s.Dir == "" || firstPrompt == ""
			})
			if firstPrompt == "" {
				continue // never used
			}
			s.Title = claudeTitle(path)
			if s.Title == "" {
				s.Title = oneLine(firstPrompt, 80)
			}
			out = append(out, s)
		}
	}
	return out
}

func claudeTitle(path string) string {
	var custom, ai string
	eachLine(readTail(path, 512*1024), func(o map[string]any) bool {
		switch str(o, "type") {
		case "custom-title":
			custom = str(o, "customTitle")
		case "ai-title":
			ai = str(o, "aiTitle")
		case "summary":
			if ai == "" {
				ai = str(o, "summary")
			}
		}
		return true
	})
	if custom != "" {
		return custom
	}
	return ai
}

// StripClaude keeps only the main-thread user and assistant records, drops text the tool
// injected (system reminders, CLAUDE.md, memory), relinks parentUuid past dropped records,
// and gives the copy a new session ID and working folder.
//
// The raw transcript also holds the full system prompt, CLAUDE.md, rules, memory, the skill
// list and MCP instructions. Never share it as is.
func StripClaude(data []byte, newID, newDir string) ([]byte, error) {
	var records []map[string]any
	parent := map[string]string{}
	eachLine(data, func(o map[string]any) bool {
		if id := str(o, "uuid"); id != "" {
			parent[id] = str(o, "parentUuid")
		}
		records = append(records, o)
		return true
	})

	var kept []map[string]any
	keptIDs := map[string]bool{}
	for _, o := range records {
		t := str(o, "type")
		if (t != "user" && t != "assistant") || o["isSidechain"] == true || o["isMeta"] == true {
			continue
		}
		msg := obj(o, "message")
		if msg == nil {
			continue
		}
		if t == "user" {
			if !cleanClaudeUserContent(msg) {
				continue
			}
		}
		clean := map[string]any{}
		for _, key := range []string{"type", "uuid", "parentUuid", "timestamp", "message", "isSidechain", "userType", "gitBranch", "version"} {
			if v, ok := o[key]; ok {
				clean[key] = v
			}
		}
		if t == "assistant" {
			cleanMsg := map[string]any{}
			for _, key := range []string{"id", "type", "role", "model", "content", "stop_reason"} {
				if v, ok := msg[key]; ok {
					cleanMsg[key] = v
				}
			}
			// Thinking blocks are signed for the sender's account. Drop them.
			if blocks, ok := cleanMsg["content"].([]any); ok {
				var keep []any
				for _, block := range blocks {
					if b, ok := block.(map[string]any); ok && strings.Contains(str(b, "type"), "thinking") {
						continue
					}
					keep = append(keep, block)
				}
				if len(keep) == 0 {
					continue
				}
				cleanMsg["content"] = keep
			}
			clean["message"] = cleanMsg
		}
		kept = append(kept, clean)
		keptIDs[str(o, "uuid")] = true
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, o := range kept {
		p := str(o, "parentUuid")
		for p != "" && !keptIDs[p] {
			p = parent[p]
		}
		if p == "" {
			o["parentUuid"] = nil
		} else {
			o["parentUuid"] = p
		}
		o["sessionId"] = newID
		o["cwd"] = newDir
		if err := enc.Encode(o); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// cleanClaudeUserContent drops injected text blocks from a user message.
// It returns false when nothing the person wrote (or a tool result) remains.
func cleanClaudeUserContent(msg map[string]any) bool {
	switch c := msg["content"].(type) {
	case string:
		return !isInjected(c)
	case []any:
		var keep []any
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			switch str(b, "type") {
			case "text":
				if !isInjected(str(b, "text")) {
					keep = append(keep, b)
				}
			case "tool_result":
				keep = append(keep, b)
			}
		}
		msg["content"] = keep
		return len(keep) > 0
	}
	return false
}

// ClaudeMessages reads the conversation as plain text turns.
func ClaudeMessages(data []byte) []Message {
	var out []Message
	eachLine(data, func(o map[string]any) bool {
		t := str(o, "type")
		if (t != "user" && t != "assistant") || o["isSidechain"] == true || o["isMeta"] == true {
			return true
		}
		text := textOf(obj(o, "message")["content"])
		if t == "user" && isInjected(text) {
			return true
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, Message{Role: t, Text: strings.TrimSpace(text)})
		}
		return true
	})
	return out
}
