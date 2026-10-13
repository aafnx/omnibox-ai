package docsearch

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStem(t *testing.T) {
	for _, pair := range [][2]string{{"договор", "договора"}, {"цена", "цены"}, {"паспорт", "паспорта"}, {"дом", "дома"}} {
		if Stem(pair[0]) != Stem(pair[1]) {
			t.Fatalf("%s ≠ %s: %s %s", pair[0], pair[1], Stem(pair[0]), Stem(pair[1]))
		}
	}
	if Stem("2024") != "2024" {
		t.Fatal("numbers stay whole")
	}
	if got := Terms("Что это за цена, и где договор?"); strings.Join(got, " ") != "цен догово" {
		t.Fatalf("terms %v", got)
	}
}

func TestSplit(t *testing.T) {
	text := strings.Repeat("Первое предложение абзаца. ", 100) + "\n\nКороткий абзац.\n" + strings.Repeat("слово ", 50)
	parts := Split(text, 300)
	if len(parts) < 3 {
		t.Fatalf("parts %d", len(parts))
	}
	joined := 0
	for _, p := range parts {
		if n := utf8.RuneCountInString(p); n > 300 || n == 0 {
			t.Fatalf("part of %d runes: %q", n, p)
		}
		joined += len(strings.Fields(p))
	}
	if joined != len(strings.Fields(text)) {
		t.Fatalf("words lost: %d of %d", joined, len(strings.Fields(text)))
	}
	if !strings.HasSuffix(parts[0], ".") {
		t.Fatalf("cut not at a sentence end: %q", parts[0])
	}
}

func TestPick(t *testing.T) {
	docs := []Doc{{Name: "a.txt", Text: "короткий"}, {Name: "b.txt", Text: "ещё"}}
	if pieces, whole := Pick(docs, "что угодно", 100); !whole || len(pieces) != 2 || pieces[1].Text != "ещё" {
		t.Fatalf("whole %v %+v", whole, pieces)
	}
	filler := strings.Repeat("Общие положения о порядке работы. ", 60)
	docs = []Doc{
		{Name: "устав.txt", Text: filler + "\n" + filler},
		{Name: "договор.pdf", Text: filler + "\nСтоимость работ по договору составляет 250 000 рублей.\n" + filler},
	}
	pieces, whole := Pick(docs, "Какая стоимость работ в договоре?", 2*ChunkRunes)
	if whole {
		t.Fatal("documents larger than the budget were given whole")
	}
	found := false
	total := 0
	for _, p := range pieces {
		total += utf8.RuneCountInString(p.Text)
		if p.Doc == "договор.pdf" && strings.Contains(p.Text, "250 000") {
			found = true
		}
	}
	if !found || total > 2*ChunkRunes {
		t.Fatalf("picked %d runes, found %v: %+v", total, found, pieces)
	}
	// Without matching words the beginnings of the documents come.
	pieces, _ = Pick(docs, "zzz", 2*ChunkRunes)
	if len(pieces) == 0 || pieces[0].Part != 0 {
		t.Fatalf("beginnings %+v", pieces)
	}
}

func TestRank(t *testing.T) {
	pieces := []Piece{{Text: "погода в Москве"}, {Text: "налог на дом и налоговый вычет"}, {Text: "вычет за лечение"}}
	order := Rank(pieces, "налоговый вычет")
	if len(order) != 2 || order[0] != 1 {
		t.Fatalf("order %v", order)
	}
	if Rank(pieces, "и в на") != nil {
		t.Fatal("stop words ranked")
	}
}
