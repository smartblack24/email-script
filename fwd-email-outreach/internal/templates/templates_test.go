package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderMissingPlaceholdersDoNotMalfom(t *testing.T) {
	dir := t.TempDir()
	content := "Subject: Hello {{.FirstName}}\n\nHi {{.FirstName}} {{.Country}}, from {{.BusinessName}}.\n"
	if err := os.WriteFile(filepath.Join(dir, "initial.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	subject, body, err := r.Render("initial.txt", Data{FirstName: "Ada", BusinessName: "FWD"})
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Hello Ada" {
		t.Fatalf("subject=%q", subject)
	}
	if strings.Contains(body, "<no value>") || strings.Contains(body, "{{") {
		t.Fatalf("malformed body: %q", body)
	}
	if !strings.Contains(body, "Hi Ada") {
		t.Fatalf("body=%q", body)
	}
}

func TestComplianceFooterInjected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "initial.txt"), []byte("Subject: Hi\n\nBody only.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := r.Render("initial.txt", Data{
		BusinessName:    "FWD Consulting Group",
		BusinessAddress: "123 Main St",
		OptOutEmail:     "optout@fwd.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "123 Main St") || !strings.Contains(body, "optout@fwd.example") {
		t.Fatalf("missing compliance footer: %q", body)
	}
}
