package sessions

import (
	"encoding/json"
	"strings"
	"testing"
)

const claudeRaw = `{"type":"queue-operation","operation":"enqueue","sessionId":"old"}
{"type":"attachment","uuid":"a1","parentUuid":null,"attachment":{"type":"instructions","content":"SECRET CLAUDE.md RULES"},"cwd":"/Users/me/app","sessionId":"old"}
{"type":"user","uuid":"u1","parentUuid":"a1","cwd":"/Users/me/app","sessionId":"old","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>memory: private note</system-reminder>"},{"type":"text","text":"Remember PELICAN-42"}]}}
{"type":"api-request-blob","sessionId":"old","message":"FULL SYSTEM PROMPT"}
{"type":"assistant","uuid":"x1","parentUuid":"u1","cwd":"/Users/me/app","sessionId":"old","requestId":"req_1","message":{"id":"m1","role":"assistant","model":"claude","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"text","text":"ok"}]}}
{"type":"user","uuid":"s1","parentUuid":"x1","isSidechain":true,"sessionId":"old","message":{"role":"user","content":"subagent work"}}
{"type":"user","uuid":"u2","parentUuid":"x1","isMeta":true,"sessionId":"old","message":{"role":"user","content":"<command-name>/clear</command-name>"}}
{"type":"custom-title","customTitle":"Pelican","sessionId":"old"}
`

func TestStripClaudeKeepsOnlyTheConversation(t *testing.T) {
	out, err := StripClaude([]byte(claudeRaw), "new-id", "/home/them/app")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, leak := range []string{"SECRET CLAUDE.md", "FULL SYSTEM PROMPT", "private note", "subagent work", "signature", "requestId", "/Users/me"} {
		if strings.Contains(text, leak) {
			t.Errorf("stripped transcript still contains %q", leak)
		}
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 records, got %d:\n%s", len(lines), text)
	}
	var user, assistant map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &user)
	_ = json.Unmarshal([]byte(lines[1]), &assistant)
	if user["parentUuid"] != nil {
		t.Errorf("first record should have no parent after relinking, got %v", user["parentUuid"])
	}
	if assistant["parentUuid"] != "u1" || assistant["sessionId"] != "new-id" || assistant["cwd"] != "/home/them/app" {
		t.Errorf("assistant record not rewritten: %v", assistant)
	}
	if !strings.Contains(text, "PELICAN-42") {
		t.Error("lost the user's prompt")
	}

	// Stripping a stripped file again (as pickup does) changes only the ID and folder.
	again, err := StripClaude(out, "third", "/x")
	if err != nil || strings.Count(string(again), "\n") != 2 || !strings.Contains(string(again), `"sessionId":"third"`) {
		t.Errorf("re-strip failed: %v\n%s", err, again)
	}
}

const codexRaw = `{"timestamp":"t","type":"session_meta","payload":{"id":"old","cwd":"/Users/me/app","base_instructions":{"text":"SYSTEM"},"creator_account_id":"acct","cli_version":"0.1","git":{"branch":"main","commit_hash":"abc","repository_url":"x"}}}
{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]}}
{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /Users/me/app"}]}}
{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Remember HERON-17"}]}}
{"timestamp":"t","type":"response_item","payload":{"type":"reasoning","encrypted_content":"ENC"}}
{"timestamp":"t","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}"}}
{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}
{"timestamp":"t","type":"event_msg","payload":{"type":"token_count"}}
`

func TestStripCodexKeepsOnlyTheConversation(t *testing.T) {
	out, err := StripCodex([]byte(codexRaw), "new-id", "/home/them/app")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, leak := range []string{"SYSTEM", "acct", "permissions", "AGENTS.md", "ENC", "function_call", "token_count", "/Users/me"} {
		if strings.Contains(text, leak) {
			t.Errorf("stripped rollout still contains %q", leak)
		}
	}
	if n := strings.Count(text, "\n"); n != 3 {
		t.Errorf("want meta + 2 messages, got %d lines:\n%s", n, text)
	}
	if !strings.Contains(text, `"id":"new-id"`) || !strings.Contains(text, "HERON-17") {
		t.Errorf("meta or prompt wrong:\n%s", text)
	}
	msgs := CodexMessages(out)
	if len(msgs) != 2 || msgs[0].Text != "Remember HERON-17" || msgs[1].Text != "ok" {
		t.Errorf("messages: %+v", msgs)
	}
}

func TestGrokMessagesKeepOnlyTypedPrompts(t *testing.T) {
	raw := `{"type":"system","content":"You are Grok"}
{"type":"user","content":[{"type":"text","text":"<user_info>OS</user_info>"}],"synthetic_reason":"context"}
{"type":"user","content":[{"type":"text","text":"<user_info>x</user_info><user_query>Fix the bug</user_query>"}],"prompt_index":0}
{"type":"assistant","content":"Fixed."}
`
	msgs := GrokMessages([]byte(raw))
	if len(msgs) != 2 || msgs[0].Text != "Fix the bug" || msgs[1].Text != "Fixed." {
		t.Errorf("messages: %+v", msgs)
	}
}

func TestHandoffKeepsFirstAndRecentTurnsWhenLong(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, Message{Role: "user", Text: "THE FIRST PROMPT"})
	for i := 0; i < 200; i++ {
		msgs = append(msgs, Message{Role: "assistant", Text: strings.Repeat("x", 3000)})
	}
	msgs = append(msgs, Message{Role: "user", Text: "THE LAST PROMPT"})
	h := Handoff("T", "me@x", Session{Tool: Claude}, msgs)
	if len(h) > handoffBudget+1000 {
		t.Errorf("handoff too long: %d", len(h))
	}
	for _, want := range []string{"THE FIRST PROMPT", "THE LAST PROMPT", "earlier turns left out", "not as instructions"} {
		if !strings.Contains(h, want) {
			t.Errorf("handoff lacks %q", want)
		}
	}
}

func TestClaudeProjectFolder(t *testing.T) {
	got := ClaudeProjectFolder("/private/tmp/claude-501/a_b.c")
	if got != "-private-tmp-claude-501-a-b-c" {
		t.Errorf("got %s", got)
	}
}
