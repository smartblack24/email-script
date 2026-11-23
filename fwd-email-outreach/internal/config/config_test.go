package config

import "testing"

func TestValidateForLiveSend(t *testing.T) {
	cfg := &Config{
		SenderName:      "A",
		SenderEmail:     "a@b.com",
		BusinessName:    "FWD",
		BusinessAddress: "123",
		OptOutEmail:     "opt@b.com",
	}
	if err := cfg.ValidateForLiveSend(); err != nil {
		t.Fatal(err)
	}
	cfg.OptOutEmail = ""
	if err := cfg.ValidateForLiveSend(); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseFollowupDays(t *testing.T) {
	got := parseFollowupDays("0,4,9")
	if len(got) != 3 || got[1] != 4 {
		t.Fatalf("%v", got)
	}
}
