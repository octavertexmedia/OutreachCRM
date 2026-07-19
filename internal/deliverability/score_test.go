package deliverability

import "testing"

func TestScoreDomain_IgnoresSenderAuth(t *testing.T) {
	withAuth := ScoreDomain("acme-widgets.io", AuthStatus{SPF: true, DKIM: true, DMARC: true}, "aspmx.l.google.com")
	without := ScoreDomain("acme-widgets.io", AuthStatus{}, "aspmx.l.google.com")
	if withAuth != without {
		t.Fatalf("recipient domain score must ignore sender auth: %v vs %v", withAuth, without)
	}
}

func TestScoreSendingAuth(t *testing.T) {
	s := ScoreSendingAuth(AuthStatus{SPF: true, DKIM: true, DMARC: true})
	if s < 90 {
		t.Fatalf("full auth should score high, got %v", s)
	}
	bad := ScoreSendingAuth(AuthStatus{Blacklisted: true})
	if bad >= 40 {
		t.Fatalf("blacklisted send auth should be low, got %v", bad)
	}
}
