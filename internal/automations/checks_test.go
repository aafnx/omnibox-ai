package automations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckValue(t *testing.T) {
	for _, c := range []struct{ check, value, want string }{
		{"passport_series", "92 04", ""}, {"passport_series", "9204", ""}, {"passport_series", "920", "4 цифры"}, {"passport_series", "92О4", "4 цифры"},
		{"passport_number", "123456", ""}, {"passport_number", "12345", "6 цифр"},
		{"division_code", "160-005", ""}, {"division_code", "160005", "123-456"},
		{"date", "12.10.2026", ""}, {"date", "«12» октября 2026 г.", ""}, {"date", "31.02.2026", "дату"}, {"date_past", "01.01.2999", "будущем"},
		{"inn", "7707083893", ""}, {"inn", "7707083894", "ИНН"}, {"inn", "500100732259", ""},
		{"snils", "112-233-445 95", ""}, {"snils", "112-233-445 96", "СНИЛС"},
		{"digits", "12 34", ""}, {"digits", "12a", "цифры"}, {"passport_series", "", ""},
	} {
		got := CheckValue(c.check, c.value)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %q: %q", c.check, c.value, got)
		}
	}
}

func TestVars(t *testing.T) {
	p := "Новости о {{ тема }} за {{период}}; ещё раз {{тема}}."
	if v := Vars(p); strings.Join(v, "|") != "тема|период" {
		t.Fatal(v)
	}
	if _, err := FillVars(p, map[string]string{"тема": "ИИ"}); err == nil || !strings.Contains(err.Error(), "период") {
		t.Fatal(err)
	}
	if out, err := FillVars(p, map[string]string{"тема": "ИИ", "период": "неделю"}); err != nil || out != "Новости о ИИ за неделю; ещё раз ИИ." {
		t.Fatal(out, err)
	}
}

func TestScheduleNext(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	mon := time.Date(2026, 10, 12, 9, 0, 0, 0, loc) // Monday
	for _, c := range []struct {
		s    Schedule
		want string
	}{
		{Schedule{Enabled: true, Every: "day", At: "08:30"}, "2026-10-13 08:30"},
		{Schedule{Enabled: true, Every: "day", At: "10:00"}, "2026-10-12 10:00"},
		{Schedule{Enabled: true, Every: "week", At: "08:00", Days: []int{5}}, "2026-10-16 08:00"},
		{Schedule{Enabled: true, Every: "week", At: "09:30", Days: []int{1, 3}}, "2026-10-12 09:30"},
		{Schedule{Enabled: true, Every: "hours", At: "00:00", Hours: 4}, "2026-10-12 12:00"},
	} {
		got, ok := c.s.Next(mon)
		if !ok || got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("%+v: %v %v", c.s, got, ok)
		}
	}
	if _, ok := (&Schedule{Enabled: false, Every: "day", At: "08:00"}).Next(mon); ok {
		t.Fatal("a disabled schedule runs")
	}
}

// Private means nothing leaves the PC and nothing of a run stays: search, history and the
// schedule are switched off whatever the panel sent.
func TestPrivacyAndScheduleRules(t *testing.T) {
	a := Automation{Name: "Сводка", Kind: KindTask, Prompt: "Новости {{тема}}", Private: true, Web: true, Read: true, History: true,
		Schedule: &Schedule{Enabled: true, Every: "day", At: "08:00"}}
	if err := check(&a); err != nil || a.Web || a.Read || a.History || a.Schedule.Enabled {
		t.Fatalf("%v %+v", err, a)
	}
	a = Automation{Name: "Сводка", Kind: KindTask, Prompt: "Новости {{тема}}", Web: true, Schedule: &Schedule{Enabled: true, Every: "day", At: "08:00"}}
	if err := check(&a); err == nil || !strings.Contains(err.Error(), "историю") {
		t.Fatal(err)
	}
	a.History, a.Model = true, "cloud:o/m"
	if err := check(&a); err == nil || !strings.Contains(err.Error(), "тема") {
		t.Fatal(err)
	}
	a.Vars = map[string]string{"тема": "ИИ"}
	if err := check(&a); err != nil {
		t.Fatal(err)
	}
	f := Automation{Name: "Паспорт", Kind: KindFields, Fields: []Field{{Name: "Серия", Check: "nonsense"}}}
	if err := check(&f); err == nil {
		t.Fatal("an unknown check is refused")
	}
	f.Schedule = &Schedule{Enabled: true, Every: "day", At: "08:00"}
	f.Fields[0].Check, f.History, f.Model = "passport_series", true, "x"
	if err := check(&f); err == nil || !strings.Contains(err.Error(), "только задачи") {
		t.Fatal(err)
	}
}

// The history keeps results with their time, the newest first, drops what is older than the
// chats' keeping time, and goes with the automation.
func TestRuns(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), Dir)}
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	a, err := s.Save(Automation{Name: "Сводка", Kind: KindTask, Prompt: "Новости", History: true}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, txt := range []string{"старый", "вчера", "сегодня"} {
		created := now.Add(time.Duration(i-2) * 24 * time.Hour)
		if i == 0 {
			created = now.Add(-40 * 24 * time.Hour)
		}
		if _, err := s.SaveRun(Run{Automation: a.ID, Created: created, Trigger: "schedule", Text: txt}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := s.Runs(a.ID, 30*24*time.Hour, now)
	if len(runs) != 2 || runs[0].Text != "сегодня" || runs[0].Seen {
		t.Fatalf("%+v", runs)
	}
	// Two runs within one second keep their order.
	for i := 0; i < 20; i++ {
		s.SaveRun(Run{Automation: a.ID, Created: now.Add(time.Duration(i) * time.Millisecond), Text: fmt.Sprint(i)})
	}
	if runs, _ := s.Runs(a.ID, 0, now); runs[0].Text != "19" || runs[1].Text != "18" {
		t.Fatalf("%s %s", runs[0].Text, runs[1].Text)
	}
	s.MarkSeen(a.ID)
	if runs, _ := s.Runs(a.ID, 0, now); !runs[0].Seen {
		t.Fatal("not marked seen")
	}
	s.Delete(a.ID)
	if runs, _ := s.Runs(a.ID, 0, now); len(runs) != 0 {
		t.Fatal("history outlived its automation")
	}
}

// The scheduler keeps one timer to the nearest run, runs what is due and sets the next one; a
// change of the automations sets it again.
func TestScheduler(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), Dir)}
	clock := time.Now()
	sched := Schedule{Enabled: true, Every: "day", At: clock.Add(2 * time.Minute).Format("15:04")}
	ran := make(chan string, 4)
	sc := &Scheduler{Store: s, Now: func() time.Time { return clock }, Run: func(_ context.Context, a Automation) { ran <- a.Name }}
	s.OnChange = sc.Reset
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc.Start(ctx)
	if !sc.Next().IsZero() {
		t.Fatal("a timer without schedules")
	}
	a, err := s.Save(Automation{Name: "Сводка", Kind: KindTask, Prompt: "Новости", History: true, Model: "cloud:o/m", Schedule: &sched}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	next := sc.Next()
	if next.IsZero() || next.Sub(clock) > 2*time.Minute || next.Sub(clock) < time.Minute {
		t.Fatalf("next %v from %v", next, clock)
	}
	// The timer's moment comes: the task runs and the next run is tomorrow's.
	clock = next
	sc.fire()
	var got string
	select {
	case got = <-ran:
	case <-time.After(5 * time.Second):
	}
	if got != "Сводка" || sc.Next().Sub(next) != 24*time.Hour {
		t.Fatalf("ran %q, next %v", got, sc.Next())
	}
	a.Schedule.Enabled = false
	s.Save(a, nil, clock)
	if !sc.Next().IsZero() {
		t.Fatal("a switched-off schedule keeps its timer")
	}
}
