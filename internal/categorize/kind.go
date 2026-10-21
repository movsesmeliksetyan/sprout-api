package categorize

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// Kind is what a statement row is (PRD §5.4).
type Kind string

// The kinds of a statement row.
const (
	KindExpense  Kind = "expense"
	KindIncome   Kind = "income"
	KindTransfer Kind = "transfer"
)

// RefundNote is the note a refund carries.
const RefundNote = "Refund"

// pairWindow is how far apart a debit and a credit of the same amount may
// be to count as one transfer.
const pairWindow = 2 * 24 * time.Hour

//go:embed data/transfer_patterns.txt
var transferPatternsFile string

// transferPatterns are the parsed transfer patterns; the file is checked
// when they are first used.
var transferPatterns = sync.OnceValues(func() ([]Keyword, error) {
	return parseTransferPatterns(transferPatternsFile)
})

func parseTransferPatterns(file string) ([]Keyword, error) {
	var patterns []Keyword
	seen := make(map[string]bool)
	for n, line := range strings.Split(file, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !validPattern(line) {
			return nil, fmt.Errorf("categorize: transfer_patterns.txt:%d: %q is not in key form", n+1, line)
		}
		if seen[line] {
			return nil, fmt.Errorf("categorize: transfer_patterns.txt:%d: %q is listed twice", n+1, line)
		}
		seen[line] = true
		patterns = append(patterns, Keyword{Pattern: line})
	}
	return patterns, nil
}

// KindInput is a normalised statement row.
type KindInput struct {
	RawDescription string
	Merchant       string
	AmountMinor    int64
	Direction      Direction
	OccurredAt     time.Time
}

// KindResult is what a row is. Transfers are left out of an import by
// default; refunds are income that keeps its merchant, with a note.
type KindResult struct {
	Kind     Kind
	Included bool
	Note     string
}

// DetectKinds decides what each of a file's rows is, in the same order:
// a transfer when its words say so, when it names the user (ownName, at
// least two words), or when a debit and a credit of the same amount fall
// within two days of each other in the file; otherwise a credit is income
// — a refund when the user spent with its merchant, in this file or
// before (knownMerchants) — and a debit is an expense.
func DetectKinds(rows []KindInput, ownName string, knownMerchants map[string]bool) ([]KindResult, error) {
	patterns, err := transferPatterns()
	if err != nil {
		return nil, err
	}
	name := ownNamePatterns(ownName)

	results := make([]KindResult, len(rows))
	keys := make([]string, len(rows))
	spent := make(map[string]bool)
	for i, row := range rows {
		keys[i] = Normalize(merchantSource(Input{RawDescription: row.RawDescription, Merchant: row.Merchant})).Key
		if row.Direction != DirectionCredit && keys[i] != "" {
			spent[keys[i]] = true
		}
		text := keywordText(row.RawDescription + " " + row.Merchant)
		if matchesAny(patterns, text) || matchesAny(name, text) {
			results[i] = KindResult{Kind: KindTransfer}
		}
	}
	pairTransfers(rows, keys, results)

	for i, row := range rows {
		switch {
		case results[i].Kind == KindTransfer:
		case row.Direction == DirectionCredit:
			results[i] = KindResult{Kind: KindIncome, Included: true}
			if refundOf(keys[i], spent) || refundOf(keys[i], knownMerchants) {
				results[i].Note = RefundNote
			}
		default:
			results[i] = KindResult{Kind: KindExpense, Included: true}
		}
	}
	return results, nil
}

// DetectKinds is the package's DetectKinds with the user's own history:
// the merchants they have spent with before, in one query.
// ownName is users.name.
func (e *Engine) DetectKinds(ctx context.Context, userID uuid.UUID, ownName string, rows []KindInput) ([]KindResult, error) {
	var keys []string
	seen := make(map[string]bool)
	for _, row := range rows {
		if row.Direction != DirectionCredit {
			continue
		}
		key := Normalize(merchantSource(Input{RawDescription: row.RawDescription, Merchant: row.Merchant})).Key
		for _, prefix := range prefixes(key) {
			if !seen[prefix] {
				seen[prefix] = true
				keys = append(keys, prefix)
			}
		}
	}
	known := make(map[string]bool)
	if len(keys) > 0 {
		found, err := e.q.ListExpenseMerchantKeys(ctx, db.ListExpenseMerchantKeysParams{UserID: userID, MerchantKeys: keys})
		if err != nil {
			return nil, fmt.Errorf("categorize: merchant history: %w", err)
		}
		for _, key := range found {
			known[key] = true
		}
	}
	return DetectKinds(rows, ownName, known)
}

// refundOf reports whether a credit's merchant is one of the merchants
// spent with, itself or by a prefix of whole words (`asos refund` is from
// `asos`).
func refundOf(key string, spent map[string]bool) bool {
	for _, prefix := range prefixes(key) {
		if spent[prefix] {
			return true
		}
	}
	return false
}

// ownNamePatterns are the user's name in key form, given name first and
// family name first; none for a name of one word, which would match too
// much.
func ownNamePatterns(name string) []Keyword {
	words := strings.Fields(keywordText(name))
	if len(words) < 2 {
		return nil
	}
	reversed := append([]string{words[len(words)-1]}, words[:len(words)-1]...)
	return []Keyword{{Pattern: strings.Join(words, " ")}, {Pattern: strings.Join(reversed, " ")}}
}

func matchesAny(patterns []Keyword, text string) bool {
	for _, p := range patterns {
		if p.Matches(text) {
			return true
		}
	}
	return false
}

// pairTransfers marks as transfers the debits and credits of equal amount
// within pairWindow of each other, each row in one pair at most, closest
// first. A credit from the merchant of the debit is a refund, not a pair.
func pairTransfers(rows []KindInput, keys []string, results []KindResult) {
	type pair struct {
		debit, credit int
		gap           time.Duration
	}
	var pairs []pair
	for d, debit := range rows {
		if debit.Direction == DirectionCredit || results[d].Kind == KindTransfer {
			continue
		}
		for c, credit := range rows {
			if credit.Direction != DirectionCredit || results[c].Kind == KindTransfer || credit.AmountMinor != debit.AmountMinor {
				continue
			}
			if refundOf(keys[c], map[string]bool{keys[d]: true}) {
				continue
			}
			gap := credit.OccurredAt.Sub(debit.OccurredAt).Abs()
			if gap <= pairWindow {
				pairs = append(pairs, pair{d, c, gap})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].gap < pairs[j].gap })
	used := make(map[int]bool)
	for _, p := range pairs {
		if used[p.debit] || used[p.credit] {
			continue
		}
		used[p.debit], used[p.credit] = true, true
		results[p.debit] = KindResult{Kind: KindTransfer}
		results[p.credit] = KindResult{Kind: KindTransfer}
	}
}
