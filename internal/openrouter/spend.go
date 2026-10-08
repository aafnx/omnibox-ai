package openrouter

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// SpendFile is the spend ledger under the data directory.
const SpendFile = "run/openrouter-spend.json"

// spendDays is how many days the ledger keeps, today included.
const spendDays = 90

// Sources of a cost.
const (
	SourceChat = "chat" // the panel chat
	SourceAPI  = "api"  // a /v1 client
)

const dateLayout = "2006-01-02"

// spendMu guards every ledger file: the chat and the gateway may hold separate Spend values for
// the same path.
var spendMu sync.Mutex

// Spend is the local ledger of OpenRouter costs that went through OmniBox (usage.cost of each
// response), by day and model. Days are local dates; the ledger keeps the last 90 of them.
type Spend struct {
	// Path is <data>/run/openrouter-spend.json.
	Path string
	// Now is time.Now when nil.
	Now func() time.Time
}

// Day is the spending of one date ("2006-01-02") in dollars.
type Day struct {
	Date string  `json:"date"`
	USD  float64 `json:"usd"`
}

// ModelSpend is the spending on one model in dollars.
type ModelSpend struct {
	Model string  `json:"model"`
	USD   float64 `json:"usd"`
}

type spendDay struct {
	Models  map[string]float64 `json:"models"`
	Sources map[string]float64 `json:"sources"`
}

type spendFile struct {
	Days map[string]*spendDay `json:"days"`
}

func (s Spend) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// load reads the ledger. A damaged ledger starts over: it is a convenience, not an account.
func (s Spend) load() spendFile {
	f := spendFile{}
	if data, err := platform.ReadShared(s.Path); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Days == nil {
		f.Days = map[string]*spendDay{}
	}
	return f
}

// oldest is the first date kept.
func (s Spend) oldest(today time.Time) string {
	return today.AddDate(0, 0, -(spendDays - 1)).Format(dateLayout)
}

// Add records a cost of usd dollars for model from source (SourceChat or SourceAPI) today and
// drops days older than 90. A zero cost (a free model) is not written.
func (s Spend) Add(model string, usd float64, source string) error {
	if source != SourceChat && source != SourceAPI {
		return errors.New("Неизвестный источник расхода OpenRouter.")
	}
	if model == "" || math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return errors.New("Неверная запись расхода OpenRouter.")
	}
	if usd == 0 {
		return nil
	}
	spendMu.Lock()
	defer spendMu.Unlock()
	f := s.load()
	today := s.now()
	date := today.Format(dateLayout)
	oldest := s.oldest(today)
	for d := range f.Days {
		if d < oldest || f.Days[d] == nil {
			delete(f.Days, d)
		}
	}
	day := f.Days[date]
	if day == nil {
		day = &spendDay{}
		f.Days[date] = day
	}
	if day.Models == nil {
		day.Models = map[string]float64{}
	}
	if day.Sources == nil {
		day.Sources = map[string]float64{}
	}
	day.Models[model] += usd
	day.Sources[source] += usd
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return platform.WriteAtomic(s.Path, data, 0o600)
}

// Days returns the spending of the last n days, today included, oldest first; days without
// spending are zero.
func (s Spend) Days(n int) []Day {
	n = min(max(n, 0), spendDays)
	spendMu.Lock()
	f := s.load()
	spendMu.Unlock()
	today := s.now()
	days := make([]Day, 0, n)
	for i := n - 1; i >= 0; i-- {
		date := today.AddDate(0, 0, -i).Format(dateLayout)
		var usd float64
		if d := f.Days[date]; d != nil {
			for _, v := range d.Models {
				usd += v
			}
		}
		days = append(days, Day{Date: date, USD: usd})
	}
	return days
}

// ByModel returns the spending per model over the last days, today included, largest first.
func (s Spend) ByModel(days int) []ModelSpend {
	return s.sum(days, func(d *spendDay) map[string]float64 { return d.Models })
}

// BySource returns the spending per source (SourceChat, SourceAPI) over the last days, today
// included.
func (s Spend) BySource(days int) map[string]float64 {
	sources := map[string]float64{}
	for _, v := range s.sum(days, func(d *spendDay) map[string]float64 { return d.Sources }) {
		sources[v.Model] = v.USD
	}
	return sources
}

func (s Spend) sum(days int, part func(*spendDay) map[string]float64) []ModelSpend {
	days = min(max(days, 0), spendDays)
	spendMu.Lock()
	f := s.load()
	spendMu.Unlock()
	result := []ModelSpend{}
	if days == 0 {
		return result
	}
	today := s.now()
	from := today.AddDate(0, 0, -(days - 1)).Format(dateLayout)
	to := today.Format(dateLayout)
	sums := map[string]float64{}
	for date, d := range f.Days {
		if d == nil || date < from || date > to {
			continue
		}
		for k, v := range part(d) {
			sums[k] += v
		}
	}
	for k, v := range sums {
		result = append(result, ModelSpend{Model: k, USD: v})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].USD != result[j].USD {
			return result[i].USD > result[j].USD
		}
		return result[i].Model < result[j].Model
	})
	return result
}
