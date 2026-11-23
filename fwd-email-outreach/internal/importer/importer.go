package importer

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/xuri/excelize/v2"
)

// Canonical field names.
const (
	FieldFullName       = "full_name"
	FieldEmail          = "email"
	FieldCountry        = "country"
	FieldSpecialization = "specialization"
	FieldProfileLinks   = "profile_links"
)

var defaultAliases = map[string]string{
	"fullname":                   FieldFullName,
	"name":                    FieldFullName,
	"contactname":             FieldFullName,
	"email":                   FieldEmail,
	"emailaddress":            FieldEmail,
	"mail":                    FieldEmail,
	"country":                 FieldCountry,
	"geographicregion":        FieldCountry,
	"countrygeographicregion": FieldCountry,
	"region":                  FieldCountry,
	"specialization":          FieldSpecialization,
	"specializations":         FieldSpecialization,
	"specialisation":          FieldSpecialization,
	"specialisations":         FieldSpecialization,
	"skills":                  FieldSpecialization,
	"profile":                 FieldProfileLinks,
	"profilelink":             FieldProfileLinks,
	"profileresumelink":       FieldProfileLinks,
	"profilerésumelink":       FieldProfileLinks,
	"resumelink":              FieldProfileLinks,
	"resume":                  FieldProfileLinks,
	"linkedin":                FieldProfileLinks,
	"links":                   FieldProfileLinks,
}

// RawContact is one imported row before DB persistence.
type RawContact struct {
	FullName        string
	FirstName       string
	Email           string
	EmailNormalized string
	Country         string
	Specialization  string
	ProfileLinks    string
	OriginalRow     map[string]string
	Valid           bool
	SkipReason      string
}

// Summary describes an import run.
type Summary struct {
	SourceFile     string
	TotalRows      int
	Valid          int
	Invalid        int
	Duplicates     int
	SkippedReasons map[string]int
	Contacts       []RawContact
	AmbiguousHeaders []string
	ColumnMap      map[string]int
}

// Options configures import behavior.
type Options struct {
	ColumnMapOverrides map[string]string // normalized header -> canonical field
}

var brTagRe = regexp.MustCompile(`(?i)<br\s*/?>`)
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)
var spaceRe = regexp.MustCompile(`\s+`)

// ImportFile loads contacts from .xlsx or .csv.
func ImportFile(path string, opts Options) (*Summary, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".xlsx", ".xlsm", ".xls":
		return importExcel(path, opts)
	case ".csv":
		return importCSV(path, opts)
	default:
		return nil, fmt.Errorf("unsupported file type %q (use .xlsx or .csv)", ext)
	}
}

func importCSV(path string, opts Options) (*Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	if len(rows) == 0 {
		return &Summary{SourceFile: path, SkippedReasons: map[string]int{}, ColumnMap: map[string]int{}}, nil
	}

	headers := rows[0]
	colMap, ambiguous, err := mapHeaders(headers, opts.ColumnMapOverrides)
	if err != nil {
		return nil, err
	}

	sum := &Summary{
		SourceFile:       path,
		SkippedReasons:   map[string]int{},
		ColumnMap:        colMap,
		AmbiguousHeaders: ambiguous,
	}

	seen := map[string]bool{}
	for i := 1; i < len(rows); i++ {
		row := rows[i]
		original := rowToMap(headers, row)
		rc := buildContact(original, colMap, headers, row, nil)
		finalizeContact(rc, seen, sum)
	}
	return sum, nil
}

func importExcel(path string, opts Options) (*Summary, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open excel: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("excel file has no sheets")
	}
	sheet := sheets[0]

	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil, fmt.Errorf("read excel rows: %w", err)
	}
	if len(rows) == 0 {
		return &Summary{SourceFile: path, SkippedReasons: map[string]int{}, ColumnMap: map[string]int{}}, nil
	}

	headers := rows[0]
	colMap, ambiguous, err := mapHeaders(headers, opts.ColumnMapOverrides)
	if err != nil {
		return nil, err
	}

	hyperlinks := collectHyperlinks(f, sheet, len(rows), len(headers))

	sum := &Summary{
		SourceFile:       path,
		SkippedReasons:   map[string]int{},
		ColumnMap:        colMap,
		AmbiguousHeaders: ambiguous,
	}
	seen := map[string]bool{}
	for i := 1; i < len(rows); i++ {
		row := rows[i]
		original := rowToMap(headers, row)
		rc := buildContact(original, colMap, headers, row, hyperlinks[i+1]) // excel rows are 1-indexed; header is row 1
		finalizeContact(rc, seen, sum)
	}
	return sum, nil
}

func collectHyperlinks(f *excelize.File, sheet string, rowCount, colCount int) map[int]map[int]string {
	out := map[int]map[int]string{}
	for r := 2; r <= rowCount; r++ {
		for c := 1; c <= colCount; c++ {
			cell, err := excelize.CoordinatesToCellName(c, r)
			if err != nil {
				continue
			}
			ok, link, err := f.GetCellHyperLink(sheet, cell)
			if err != nil || !ok || strings.TrimSpace(link) == "" {
				continue
			}
			if out[r] == nil {
				out[r] = map[int]string{}
			}
			out[r][c-1] = strings.TrimSpace(link)
		}
	}
	return out
}

func mapHeaders(headers []string, overrides map[string]string) (map[string]int, []string, error) {
	colMap := map[string]int{}
	ambiguous := []string{}
	claimed := map[int]string{}

	for i, h := range headers {
		norm := NormalizeHeader(h)
		if norm == "" {
			continue
		}
		field := ""
		if overrides != nil {
			if v, ok := overrides[norm]; ok {
				field = v
			}
		}
		if field == "" {
			if v, ok := defaultAliases[norm]; ok {
				field = v
			}
		}
		if field == "" {
			// fuzzy contains matching
			field = fuzzyMatch(norm)
		}
		if field == "" {
			continue
		}
		if prev, ok := colMap[field]; ok && prev != i {
			ambiguous = append(ambiguous, fmt.Sprintf("%s (columns %d and %d)", field, prev+1, i+1))
			continue
		}
		if other, ok := claimed[i]; ok && other != field {
			ambiguous = append(ambiguous, fmt.Sprintf("column %d maps to both %s and %s", i+1, other, field))
			continue
		}
		colMap[field] = i
		claimed[i] = field
	}

	if _, ok := colMap[FieldEmail]; !ok {
		return nil, ambiguous, fmt.Errorf("could not map Email column; provide COLUMN_MAP override (e.g. COLUMN_MAP=emailaddress=email)")
	}
	if _, ok := colMap[FieldFullName]; !ok {
		return nil, ambiguous, fmt.Errorf("could not map Full Name column; provide COLUMN_MAP override")
	}
	return colMap, ambiguous, nil
}

func fuzzyMatch(norm string) string {
	checks := []struct {
		needles []string
		field   string
	}{
		{[]string{"fullname", "contact"}, FieldFullName},
		{[]string{"email", "mail"}, FieldEmail},
		{[]string{"country", "region", "geo"}, FieldCountry},
		{[]string{"special", "skill"}, FieldSpecialization},
		{[]string{"profile", "resume", "linkedin", "cv"}, FieldProfileLinks},
	}
	for _, c := range checks {
		for _, n := range c.needles {
			if strings.Contains(norm, n) {
				return c.field
			}
		}
	}
	return ""
}

func rowToMap(headers, row []string) map[string]string {
	m := map[string]string{}
	for i, h := range headers {
		val := ""
		if i < len(row) {
			val = row[i]
		}
		m[h] = val
	}
	return m
}

func buildContact(original map[string]string, colMap map[string]int, headers, row []string, rowLinks map[int]string) *RawContact {
	get := func(field string) string {
		idx, ok := colMap[field]
		if !ok {
			return ""
		}
		if idx < len(row) {
			return row[idx]
		}
		return ""
	}

	fullName := strings.TrimSpace(get(FieldFullName))
	email := strings.TrimSpace(get(FieldEmail))
	country := strings.TrimSpace(get(FieldCountry))
	spec := NormalizeSpecializations(get(FieldSpecialization))

	linksRaw := get(FieldProfileLinks)
	// merge excel hyperlinks from the profile column and any link-like cells
	extra := []string{}
	if rowLinks != nil {
		if idx, ok := colMap[FieldProfileLinks]; ok {
			if hl, ok := rowLinks[idx]; ok {
				extra = append(extra, hl)
			}
		}
		for _, hl := range rowLinks {
			extra = append(extra, hl)
		}
	}
	links := NormalizeProfileLinks(linksRaw, extra...)

	rc := &RawContact{
		FullName:       fullName,
		FirstName:      ExtractFirstName(fullName),
		Email:          email,
		EmailNormalized: NormalizeEmail(email),
		Country:        country,
		Specialization: spec,
		ProfileLinks:   links,
		OriginalRow:    original,
		Valid:          true,
	}
	return rc
}

func finalizeContact(rc *RawContact, seen map[string]bool, sum *Summary) {
	sum.TotalRows++
	if rc.EmailNormalized == "" || !ValidEmail(rc.Email) {
		rc.Valid = false
		rc.SkipReason = "invalid_email"
		sum.Invalid++
		sum.SkippedReasons[rc.SkipReason]++
		sum.Contacts = append(sum.Contacts, *rc)
		return
	}
	if rc.FullName == "" {
		rc.Valid = false
		rc.SkipReason = "missing_name"
		sum.Invalid++
		sum.SkippedReasons[rc.SkipReason]++
		sum.Contacts = append(sum.Contacts, *rc)
		return
	}
	if seen[rc.EmailNormalized] {
		rc.Valid = false
		rc.SkipReason = "duplicate_email"
		sum.Duplicates++
		sum.SkippedReasons[rc.SkipReason]++
		sum.Contacts = append(sum.Contacts, *rc)
		return
	}
	seen[rc.EmailNormalized] = true
	sum.Valid++
	sum.Contacts = append(sum.Contacts, *rc)
}

// NormalizeHeader lowercases and strips spaces/punctuation for mapping.
func NormalizeHeader(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeEmail lowercases and trims.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidEmail performs a practical RFC-ish check.
func ValidEmail(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" || strings.Count(email, "@") != 1 {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil
}

// ExtractFirstName takes the first token of a full name.
func ExtractFirstName(full string) string {
	full = strings.TrimSpace(full)
	if full == "" {
		return ""
	}
	parts := strings.Fields(full)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// NormalizeSpecializations splits on commas and rejoins cleaned values.
func NormalizeSpecializations(raw string) string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// NormalizeProfileLinks extracts URLs from plain text, commas, newlines, and HTML breaks.
func NormalizeProfileLinks(raw string, extras ...string) string {
	combined := raw
	for _, e := range extras {
		if strings.TrimSpace(e) != "" {
			combined += "\n" + e
		}
	}
	combined = brTagRe.ReplaceAllString(combined, "\n")
	combined = htmlTagRe.ReplaceAllString(combined, " ")
	combined = strings.ReplaceAll(combined, ",", "\n")
	combined = strings.ReplaceAll(combined, ";", "\n")
	combined = spaceRe.ReplaceAllString(combined, " ")

	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(combined, "\n") {
		for _, tok := range strings.Fields(line) {
			tok = strings.TrimSpace(tok)
			tok = strings.Trim(tok, "<>\"'")
			if tok == "" {
				continue
			}
			low := strings.ToLower(tok)
			if !(strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") || strings.Contains(tok, ".")) {
				continue
			}
			if seen[tok] {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return strings.Join(out, "\n")
}

// OriginalJSON serializes the original row map.
func OriginalJSON(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// NowUTC returns current UTC time (overridable in tests via package if needed).
func NowUTC() time.Time {
	return time.Now().UTC()
}
