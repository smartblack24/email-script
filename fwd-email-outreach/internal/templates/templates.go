package templates

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// Data is the template execution context.
type Data struct {
	FirstName       string
	FullName        string
	Email           string
	Country         string
	Specialization  string
	ProfileLinks    string
	SenderName      string
	SenderEmail     string
	BusinessName    string
	BusinessAddress string
	OptOutEmail     string
}

// Renderer loads and executes editable template files.
type Renderer struct {
	dir *template.Template
	root string
}

// Load reads templates from dir (initial.txt, followup1.txt, followup2.txt, …).
func Load(dir string) (*Renderer, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read templates dir: %w", err)
	}
	r := &Renderer{root: dir}
	funcs := template.FuncMap{
		"joinLinks": func(s string) string {
			parts := strings.Split(s, "\n")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					out = append(out, p)
				}
			}
			return strings.Join(out, ", ")
		},
	}
	t := template.New("root").Funcs(funcs).Option("missingkey=zero")
	found := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".txt") && !strings.HasSuffix(name, ".html") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if _, err := t.New(name).Parse(string(b)); err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		found++
	}
	if found == 0 {
		return nil, fmt.Errorf("no templates found in %s", dir)
	}
	r.dir = t
	return r, nil
}

// Render returns subject and body for a named template file.
// Subject is the first line if it starts with "Subject:"; otherwise a default is used.
func (r *Renderer) Render(name string, data Data) (subject, body string, err error) {
	var buf bytes.Buffer
	if err := r.dir.ExecuteTemplate(&buf, name, data); err != nil {
		return "", "", fmt.Errorf("execute %s: %w", name, err)
	}
	raw := buf.String()
	lines := strings.SplitN(raw, "\n", 2)
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "Subject:") {
		subject = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), "Subject:"))
		if len(lines) > 1 {
			body = strings.TrimLeft(lines[1], "\r\n")
		}
	} else {
		subject = fmt.Sprintf("Introduction from %s", orDefault(data.BusinessName, "FWD Consulting Group"))
		body = raw
	}
	body = ensureComplianceFooter(body, data)
	return subject, body, nil
}

// TemplateForStep maps follow-up step index to template filename.
func TemplateForStep(step int) string {
	switch step {
	case 0:
		return "initial.txt"
	case 1:
		return "followup1.txt"
	default:
		return "followup2.txt"
	}
}

func ensureComplianceFooter(body string, data Data) string {
	lower := strings.ToLower(body)
	needOpt := data.OptOutEmail != "" && !strings.Contains(lower, strings.ToLower(data.OptOutEmail))
	needAddr := data.BusinessAddress != "" && !strings.Contains(lower, strings.ToLower(data.BusinessAddress))
	if !needOpt && !needAddr {
		return body
	}
	var footer strings.Builder
	footer.WriteString("\n\n--\n")
	if data.BusinessName != "" {
		footer.WriteString(data.BusinessName)
		footer.WriteString("\n")
	}
	if data.BusinessAddress != "" {
		footer.WriteString(data.BusinessAddress)
		footer.WriteString("\n")
	}
	if data.OptOutEmail != "" {
		footer.WriteString("To opt out of future emails, reply or write to ")
		footer.WriteString(data.OptOutEmail)
		footer.WriteString(".\n")
	}
	return body + footer.String()
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
