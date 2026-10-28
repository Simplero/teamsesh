// Package sessions finds local assistant sessions and turns them into shareable form.
package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

type Tool string

const (
	Claude Tool = "claude"
	Codex  Tool = "codex"
	Grok   Tool = "grok"
)

// Session is one local conversation.
type Session struct {
	Tool      Tool
	ID        string
	Title     string
	Dir       string // the working folder the session ran in
	Branch    string
	Path      string // the transcript file
	UpdatedAt time.Time
	Size      int64
}

// Message is one user or assistant turn, as plain text.
type Message struct {
	Role string // "user" or "assistant"
	Text string
}

// Find lists local sessions for every supported tool, newest first.
func Find() []Session {
	var all []Session
	all = append(all, findClaude()...)
	all = append(all, findCodex()...)
	all = append(all, findGrok()...)
	sort.Slice(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	return all
}

// Resolve finds a session by full ID or an unambiguous ID prefix.
func Resolve(all []Session, ref string) (Session, bool, []Session) {
	var hits []Session
	for _, s := range all {
		if s.ID == ref {
			return s, true, nil
		}
		if len(ref) >= 6 && strings.HasPrefix(s.ID, ref) {
			hits = append(hits, s)
		}
	}
	if len(hits) == 1 {
		return hits[0], true, nil
	}
	return Session{}, false, hits
}

// Latest returns the newest session that ran in dir.
func Latest(all []Session, dir string) (Session, bool) {
	dir = cleanDir(dir)
	for _, s := range all {
		if cleanDir(s.Dir) == dir {
			return s, true
		}
	}
	return Session{}, false
}

func cleanDir(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return filepath.Clean(dir)
}

// ReadFile reads a transcript, decompressing .zst files.
func ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(path, ".zst") {
		return data, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(data, nil)
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// eachLine calls fn for each JSON object line. It stops when fn returns false.
func eachLine(data []byte, fn func(map[string]any) bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	for sc.Scan() {
		var obj map[string]any
		if json.Unmarshal(sc.Bytes(), &obj) != nil {
			continue
		}
		if !fn(obj) {
			return
		}
	}
}

// readHead reads up to n bytes from the start of a file (decompressed, for .zst files).
func readHead(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".zst") {
		dec, err := zstd.NewReader(f)
		if err != nil {
			return nil
		}
		defer dec.Close()
		r = dec
	}
	data, _ := io.ReadAll(io.LimitReader(r, n))
	return data
}

// readTail reads up to n bytes from the end of a file, starting at a line boundary.
func readTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	start := info.Size() - n
	if start <= 0 {
		data, _ := io.ReadAll(f)
		return data
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, _ := io.ReadAll(f)
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return data[i+1:]
	}
	return nil
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func obj(m map[string]any, key string) map[string]any {
	o, _ := m[key].(map[string]any)
	return o
}

// textOf joins the text blocks of a message content field (a string or a list of blocks).
func textOf(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			switch str(b, "type") {
			case "text", "input_text", "output_text":
				parts = append(parts, str(b, "text"))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// isInjected reports text that the tool added, not the person.
func isInjected(text string) bool {
	t := strings.TrimSpace(text)
	for _, prefix := range []string{"<system-reminder>", "<environment_context>", "<user_instructions>", "<user_info>",
		"# AGENTS.md", "<permissions", "<command-name>", "<local-command", "<command-message>", "Caveat:", "<rules>"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return t == ""
}

// stripQueryTags removes the <user_query> wrapper some tools put around prompts.
func stripQueryTags(text string) string {
	if i := strings.Index(text, "<user_query>"); i >= 0 {
		text = text[i+len("<user_query>"):]
		if j := strings.Index(text, "</user_query>"); j >= 0 {
			text = text[:j]
		}
	}
	return strings.TrimSpace(text)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > max {
		return string([]rune(s)[:max-1]) + "…"
	}
	return s
}
