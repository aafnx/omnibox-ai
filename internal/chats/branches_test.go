package chats

import (
	"path/filepath"
	"testing"
)

func texts(list []Message) []string {
	var out []string
	for _, m := range list {
		out = append(out, m.Text)
	}
	return out
}

// Answering again or editing a question keeps the earlier version with what followed it; any
// version can be shown again, and the costs of all of them count.
func TestBranches(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), "chats")}
	c, _ := s.Create("", 24, "", "")
	c, err := s.Update(c.ID, func(c *Chat) error {
		c.Messages = []Message{{ID: "aaaaaaaaaaaa", Role: "user", Text: "Q1"}, {Role: "assistant", Text: "A1", Meta: &Meta{CostUSD: 0.01}},
			{ID: "bbbbbbbbbbbb", Role: "user", Text: "Q2"}, {Role: "assistant", Text: "A2"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Edit Q1: the old Q1 with A1, Q2, A2 becomes version 1 of 2.
	c, err = s.Update(c.ID, func(c *Chat) error {
		next := c.Messages[0]
		next.Text = "Q1 правка"
		Fork(c, 0, next)
		c.Messages = append(c.Messages, Message{Role: "assistant", Text: "B1", Meta: &Meta{CostUSD: 0.02}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(c.Messages); len(got) != 2 || got[0] != "Q1 правка" || got[1] != "B1" || c.Messages[0].Branch != 1 || len(c.Messages[0].Branches) != 1 {
		t.Fatalf("after the edit: %v %+v", got, c.Messages[0])
	}
	if c.CostUSD != 0.03 {
		t.Fatalf("cost of all versions: %v", c.CostUSD)
	}
	// Back to version 0: the whole earlier conversation returns; the edit is kept as version 1.
	c, err = s.SwitchBranch(c.ID, "aaaaaaaaaaaa", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(c.Messages); len(got) != 4 || got[0] != "Q1" || got[3] != "A2" || c.Messages[0].Branch != 0 || texts(c.Messages[0].Branches[0])[1] != "B1" {
		t.Fatalf("version 0: %v", got)
	}
	c, _ = s.SwitchBranch(c.ID, "aaaaaaaaaaaa", 1)
	if got := texts(c.Messages); got[0] != "Q1 правка" || got[1] != "B1" || c.CostUSD != 0.03 {
		t.Fatalf("version 1: %v", got)
	}
	if _, err := s.SwitchBranch(c.ID, "aaaaaaaaaaaa", 5); err == nil {
		t.Fatal("a missing version shown")
	}
	// Too many versions: the oldest goes.
	for i := 0; i < MaxBranches+2; i++ {
		c, err = s.Update(c.ID, func(c *Chat) error { Fork(c, 0, c.Messages[0]); return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := len(c.Messages[0].Branches) + 1; n != MaxBranches {
		t.Fatalf("%d versions kept", n)
	}
}
