package deliverability

import "testing"

func TestValidateSyntax(t *testing.T) {
	ok := []string{"john@gmail.com", "a.b+c@example.co.uk"}
	bad := []string{"john@@gmail.com", "john.gmail.com", "", "nope@", "@x.com"}
	for _, e := range ok {
		if !ValidateSyntax(e) {
			t.Fatalf("expected ok: %s", e)
		}
	}
	for _, e := range bad {
		if ValidateSyntax(e) {
			t.Fatalf("expected bad: %s", e)
		}
	}
}

func TestDisposableAndRole(t *testing.T) {
	if !IsDisposable("mailinator.com") {
		t.Fatal("mailinator")
	}
	if IsDisposable("gmail.com") {
		t.Fatal("gmail not disposable")
	}
	if !IsRoleBased("info") || IsRoleBased("jane") {
		t.Fatal("role")
	}
}

func TestTypo(t *testing.T) {
	if SuggestTypo("gmial.com") != "gmail.com" {
		t.Fatal("typo")
	}
}

func TestContentSpam(t *testing.T) {
	score, _ := AnalyzeContent("ACT NOW FREE MONEY!!!", "Click here buy now http://bit.ly/x http://a.com http://b.com http://c.com")
	if score < 40 {
		t.Fatalf("expected high risk, got %v", score)
	}
}

func TestWarmup(t *testing.T) {
	if WarmupDailyLimit(0, 500) != 20 {
		t.Fatal("day0")
	}
	if WarmupDailyLimit(3, 500) != 150 {
		t.Fatal("day3")
	}
	if WarmupDailyLimit(0, 10) != 10 {
		t.Fatal("cap")
	}
}

func TestPredictBounce(t *testing.T) {
	p := PredictBounce(false, false, false, false, "", RecipientHistory{}, 50, false, true)
	if p != 100 {
		t.Fatal(p)
	}
	p = PredictBounce(true, true, true, false, "", RecipientHistory{}, 100, false, true)
	if p < 90 {
		t.Fatal(p)
	}
}

func TestEngineSuppressDisposable(t *testing.T) {
	e := New(DefaultConfig())
	d := e.QuickValidate(t.Context(), "x@mailinator.com")
	if d.Action != ActionSuppress {
		t.Fatalf("%+v", d)
	}
}
