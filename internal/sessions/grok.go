package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Grok Build keeps one folder per session: ~/.grok/sessions/<encoded cwd>/<id>/.
// TeamSesh shares Grok sessions as a handoff only.
func findGrok() []Session {
	var out []Session
	dirs, _ := filepath.Glob(filepath.Join(home(), ".grok", "sessions", "*", "*"))
	for _, dir := range dirs {
		chat := filepath.Join(dir, "chat_history.jsonl")
		info, err := os.Stat(chat)
		if err != nil || info.Size() == 0 {
			continue
		}
		s := Session{Tool: Grok, ID: filepath.Base(dir), Path: chat, UpdatedAt: info.ModTime(), Size: info.Size()}
		var summary struct {
			Info struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			} `json:"info"`
			GeneratedTitle string `json:"generated_title"`
			SessionSummary string `json:"session_summary"`
			HeadBranch     string `json:"head_branch"`
		}
		if data, err := os.ReadFile(filepath.Join(dir, "summary.json")); err == nil {
			_ = json.Unmarshal(data, &summary)
		}
		if summary.Info.ID != "" {
			s.ID = summary.Info.ID
		}
		s.Dir = summary.Info.Cwd
		s.Branch = summary.HeadBranch
		s.Title = summary.GeneratedTitle
		if s.Title == "" {
			s.Title = summary.SessionSummary
		}
		msgs := GrokMessages(readHead(chat, 2*1024*1024))
		if len(msgs) == 0 {
			continue // opened and never used
		}
		if s.Title == "" {
			s.Title = oneLine(msgs[0].Text, 80)
		}
		out = append(out, s)
	}
	return out
}

// GrokMessages reads the conversation as plain text turns. Only prompts the person typed
// (records with prompt_index) count as user turns.
func GrokMessages(data []byte) []Message {
	var out []Message
	eachLine(data, func(o map[string]any) bool {
		switch str(o, "type") {
		case "user":
			if _, typed := o["prompt_index"]; !typed {
				return true
			}
			if text := stripQueryTags(textOf(o["content"])); text != "" && !isInjected(text) {
				out = append(out, Message{Role: "user", Text: text})
			}
		case "assistant":
			if text := strings.TrimSpace(textOf(o["content"])); text != "" {
				out = append(out, Message{Role: "assistant", Text: text})
			}
		}
		return true
	})
	return out
}

// Messages reads any supported session as plain text turns.
func Messages(s Session) ([]Message, error) {
	data, err := ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	switch s.Tool {
	case Claude:
		return ClaudeMessages(data), nil
	case Codex:
		return CodexMessages(data), nil
	default:
		return GrokMessages(data), nil
	}
}
