package imapsync

import "testing"

func TestApplyPresetFillsBlankHosts(t *testing.T) {
	ih, ip, sh, sp := ApplyPreset("titan", "", "", 0, 0)
	if ih != "imap.titan.email" || ip != 993 || sh != "smtp.titan.email" || sp != 465 {
		t.Fatalf("titan: %s:%d / %s:%d", ih, ip, sh, sp)
	}
}

func TestApplyPresetKeepsOverrides(t *testing.T) {
	ih, ip, sh, sp := ApplyPreset("zoho", "imap.custom.com", "smtp.custom.com", 143, 25)
	if ih != "imap.custom.com" || ip != 143 || sh != "smtp.custom.com" || sp != 25 {
		t.Fatalf("override: %s:%d / %s:%d", ih, ip, sh, sp)
	}
}

func TestPresetByIDUnknownIsCustom(t *testing.T) {
	p := PresetByID("nope")
	if p.ID != "custom" {
		t.Fatalf("id=%q", p.ID)
	}
}
