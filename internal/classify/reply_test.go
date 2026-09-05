package classify

import (
	"testing"
	"time"
)

func TestMapSmartleadCategory(t *testing.T) {
	cases := []struct {
		raw     string
		wantCat Category
		wantAsk Ask
	}{
		// The live vocabulary, observed on a real account.
		{"Interested", Interested, AskNone},
		{"Meeting Request", Interested, AskMeeting},
		{"Information Request", Interested, AskInfo},
		{"Not Interested", NotInterested, AskNone},
		{"Not Interested Now", FollowUpLater, AskNone},
		{"Out Of Office", OutOfOffice, AskNone},
		{"Wrong Person", WrongContact, AskNone},
		{"Do Not Contact", Remove, AskNone},
		{"Sender Originated Bounce", Automated, AskNone},
		{"Uncategorizable by Ai", Uncategorized, AskNone},
		// Formatting must not matter.
		{"  meeting_request  ", Interested, AskMeeting},
		{"MEETING REQUEST", Interested, AskMeeting},
		// Absent and unknown both need the AI pass, not a silent neutral.
		{"", Uncategorized, AskNone},
		{"Something Smartlead Added Later", Uncategorized, AskNone},
	}
	for _, c := range cases {
		cat, ask := MapSmartleadCategory(c.raw)
		if cat != c.wantCat || ask != c.wantAsk {
			t.Errorf("MapSmartleadCategory(%q) = %s/%s, want %s/%s", c.raw, cat, ask, c.wantCat, c.wantAsk)
		}
	}
}

// The observed failure mode: a reply three seconds after the send is a machine.
func TestFastReplyIsAutoReply(t *testing.T) {
	sent := time.Date(2025, 11, 17, 19, 39, 51, 0, time.UTC)
	r := Classify(Input{
		SentAt:    sent,
		RepliedAt: sent.Add(3 * time.Second),
		Subject:   "Re: Your 2025 story",
		Body:      "Thanks!",
	})
	if !r.AutoReply {
		t.Fatal("a reply 3 seconds after the send must be treated as automated")
	}
	if r.Scoreable() {
		t.Error("an auto-reply must not be scoreable")
	}
}

func TestSlowReplyIsNotAutoReply(t *testing.T) {
	sent := time.Now().Add(-48 * time.Hour)
	r := Classify(Input{
		SmartleadCategory: "Interested",
		SentAt:            sent,
		RepliedAt:         sent.Add(20 * time.Hour),
		Subject:           "Re: hello",
		Body:              "This looks useful, can we talk?",
	})
	if r.AutoReply {
		t.Fatalf("a reply 20h later is human: %s", r.Reason)
	}
	if r.Category != Interested {
		t.Errorf("category = %s, want interested", r.Category)
	}
}

func TestSmartleadIgnoreReplyIsTrusted(t *testing.T) {
	r := Classify(Input{IgnoreReply: true, Body: "anything"})
	if !r.AutoReply {
		t.Error("ignore_reply must mark the reply automated")
	}
}

func TestOutOfOfficeDetectedFromText(t *testing.T) {
	for _, subject := range []string{
		"Automatic reply: Your 2025 story",
		"Out of Office",
		"Undeliverable: message",
	} {
		r := Classify(Input{Subject: subject, Body: "..."})
		if !r.AutoReply {
			t.Errorf("subject %q should be detected as automated", subject)
		}
	}
	r := Classify(Input{Subject: "Re: hello", Body: "I am currently out of the office until Monday."})
	if !r.AutoReply {
		t.Error("body phrasing should be detected as automated")
	}
}

// An out-of-office that sounds keen is still an out-of-office. Reading it as
// interest is exactly the mistake that inflates a prospect's score.
func TestEnthusiasticOutOfOfficeIsStillNotInterest(t *testing.T) {
	r := Classify(Input{
		SmartleadCategory: "Interested",
		Subject:           "Automatic reply: your email",
		Body:              "Sounds interesting! I am currently out of the office until Monday.",
	})
	if !r.AutoReply {
		t.Fatal("auto-reply detection must run before the category mapping")
	}
	if r.Scoreable() {
		t.Error("must not be scoreable")
	}
	if pts, _ := IntentPoints(r); pts != 0 {
		t.Errorf("IntentPoints = %d, want 0", pts)
	}
}

func TestIntentPoints(t *testing.T) {
	cases := []struct {
		name string
		in   Result
		want int
	}{
		{"meeting", Result{Category: Interested, Ask: AskMeeting}, 55},
		{"information", Result{Category: Interested, Ask: AskInfo}, 40},
		{"positive", Result{Category: Interested}, 30},
		{"timing objection", Result{Category: FollowUpLater}, 25},
		{"not interested", Result{Category: NotInterested}, -30},
		{"wrong contact", Result{Category: WrongContact}, 0},
		{"out of office", Result{Category: OutOfOffice}, 0},
		{"auto reply", Result{Category: Interested, Ask: AskMeeting, AutoReply: true}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := IntentPoints(c.in); got != c.want {
				t.Errorf("IntentPoints = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSuppresses(t *testing.T) {
	if !Suppresses(Result{Category: Remove}) {
		t.Error("an opt-out must suppress")
	}
	// "Not interested" is a no for now, not a legal boundary.
	if Suppresses(Result{Category: NotInterested}) {
		t.Error("not_interested must not permanently suppress")
	}
}

func TestVersionIsRecorded(t *testing.T) {
	r := Classify(Input{SmartleadCategory: "Interested"})
	if r.Version != Version {
		t.Errorf("Version = %d, want %d — without it a rule change cannot be re-run selectively", r.Version, Version)
	}
}
