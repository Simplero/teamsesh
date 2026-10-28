package secrets

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestScanFindsCommonSecrets(t *testing.T) {
	cases := map[string]string{
		"GitHub token":         "token is ghp_" + strings.Repeat("a1B2", 9),
		"Anthropic key":        "export ANTHROPIC_API_KEY=sk-ant-oat01-" + strings.Repeat("Zx9", 12),
		"AWS key":              "AKIAIOSFODNN7EXAMPLE",
		"private key":          "-----BEGIN OPENSSH PRIVATE KEY-----\\nb3BlbnNzaC1rZXk\\n-----END OPENSSH PRIVATE KEY-----",
		"Authorization header": `curl -H "Authorization: Bearer abcdef0123456789ABCDEFxyz"`,
		"credential":           `DB_PASSWORD="q8Zr!kT2#vLm9Wp4xN"`,
		"credential in URL":    "postgres://app:hunter2secret@db.internal:5432/app",
	}
	for kind, text := range cases {
		hits := Scan("f", text)
		if len(hits) == 0 {
			t.Errorf("%s: no hit in %q", kind, text)
			continue
		}
		if !strings.HasPrefix(hits[0].Kind, strings.Split(kind, " ")[0]) && hits[0].Kind != kind {
			t.Errorf("%s: got kind %q", kind, hits[0].Kind)
		}
	}
}

func TestScanFindsBase64EncodedSecrets(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("my key is ghp_" + strings.Repeat("Q7w", 12)))
	if hits := Scan("f", "blob: "+encoded); len(hits) == 0 || !strings.Contains(hits[0].Kind, "base64") {
		t.Errorf("missed base64 secret: %+v", hits)
	}
}

func TestScanIgnoresOrdinaryText(t *testing.T) {
	for _, text := range []string{
		"token: /Users/calvin/code/app/config/tokens.yml",
		`"session_token_count": 12`,
		"password: changeme",
		"The API key goes in the settings page.",
	} {
		if hits := Scan("f", text); len(hits) > 0 {
			t.Errorf("false positive in %q: %+v", text, hits)
		}
	}
}

func TestRedactKeepsJSONValid(t *testing.T) {
	line := `{"message":{"content":"use sk-ant-api03-` + strings.Repeat("Ab1", 15) + ` and AKIAIOSFODNN7EXAMPLE now"}}`
	out := Redact(line, Scan("f", line))
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("redacted line is not JSON: %v\n%s", err, out)
	}
	if strings.Contains(out, "sk-ant") || strings.Contains(out, "AKIA") || strings.Count(out, "[REDACTED]") != 2 {
		t.Errorf("not redacted: %s", out)
	}
}
