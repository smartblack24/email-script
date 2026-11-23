package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestNormalizeHeaderIgnoresSpacesAndCase(t *testing.T) {
	if got := NormalizeHeader("Country / Geographic Region"); got != "countrygeographicregion" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeHeader("  Full   Name "); got != "fullname" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractFirstName(t *testing.T) {
	if got := ExtractFirstName("Ada Lovelace"); got != "Ada" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeSpecializations(t *testing.T) {
	got := NormalizeSpecializations("Math, Computing ,  AI")
	if got != "Math, Computing, AI" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeProfileLinks(t *testing.T) {
	raw := "https://a.example<br>https://b.example, https://c.example"
	got := NormalizeProfileLinks(raw, "https://a.example", "https://d.example")
	parts := strings.Split(got, "\n")
	if len(parts) != 4 {
		t.Fatalf("expected 4 unique links, got %d: %q", len(parts), got)
	}
}

func TestValidEmail(t *testing.T) {
	if !ValidEmail("ada@example.com") {
		t.Fatal("expected valid")
	}
	if ValidEmail("not-an-email") {
		t.Fatal("expected invalid")
	}
}

func TestImportCSVExactAndSpacedHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.csv")
	content := "Full Name,Email,Country / Geographic Region,Specialization,Profile / Resume Link\n" +
		"Ada Lovelace,ada@example.com,UK,\"Math, Computing\",\"https://a.example\nhttps://b.example\"\n" +
		"Bad,not-email,X,Y,\n" +
		"Ada Dup,ADA@example.com,UK,Math,https://dup.example\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := ImportFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Valid != 1 {
		t.Fatalf("valid=%d want 1", sum.Valid)
	}
	if sum.Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", sum.Duplicates)
	}
	if sum.Invalid < 1 {
		t.Fatalf("invalid=%d want >=1", sum.Invalid)
	}
	c := sum.Contacts[0]
	if c.FirstName != "Ada" || c.EmailNormalized != "ada@example.com" {
		t.Fatalf("unexpected contact %+v", c)
	}
	if !strings.Contains(c.Specialization, "Computing") {
		t.Fatalf("specialization %q", c.Specialization)
	}
	if !strings.Contains(c.ProfileLinks, "https://a.example") {
		t.Fatalf("links %q", c.ProfileLinks)
	}
}

func TestImportExcelHyperlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.xlsx")
	f := excelize.NewFile()
	sheet := "Sheet1"
	headers := []string{"Full Name", "Email", "Country / Geographic Region", "Specialization", "Profile / Resume Link"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellValue(sheet, "A2", "Grace Hopper")
	_ = f.SetCellValue(sheet, "B2", "grace@example.com")
	_ = f.SetCellValue(sheet, "C2", "US")
	_ = f.SetCellValue(sheet, "D2", "Compilers")
	_ = f.SetCellValue(sheet, "E2", "see resume")
	if err := f.SetCellHyperLink(sheet, "E2", "https://example.com/grace.pdf", "External"); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	sum, err := ImportFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Valid != 1 {
		t.Fatalf("valid=%d", sum.Valid)
	}
	if !strings.Contains(sum.Contacts[0].ProfileLinks, "https://example.com/grace.pdf") {
		t.Fatalf("expected hyperlink in links: %q", sum.Contacts[0].ProfileLinks)
	}
}

func TestSpacedWeirdHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weird.csv")
	content := "  Full   Name , EMAIL ADDRESS , Country / Geographic Region , Specialization , Profile / Resume Link\n" +
		"Test User,test@example.com,CA,DevOps,https://example.com/t\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := ImportFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Valid != 1 {
		t.Fatalf("valid=%d", sum.Valid)
	}
}
