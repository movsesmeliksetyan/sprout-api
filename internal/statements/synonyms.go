package statements

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"go.yaml.in/yaml/v3"
	"golang.org/x/text/unicode/norm"
)

//go:embed data/header_synonyms.yaml
var synonymsFile []byte

// role is what a column holds.
type role string

// The roles a header can name.
const (
	roleDate        role = "date"
	roleDescription role = "description"
	roleAmount      role = "amount"
	roleDebit       role = "debit"
	roleCredit      role = "credit"
	roleBalance     role = "balance"
	roleCurrency    role = "currency"
	roleReference   role = "reference"
	roleMCC         role = "mcc"
	roleIgnore      role = "ignore"
)

// synonyms are the parsed synonym file.
type synonyms struct {
	byLabel map[string]role
	// labels by role, for matching inside longer headers.
	labels  []labelRole
	debits  map[string]bool
	credits map[string]bool
}

type labelRole struct {
	label string
	role  role
}

var loadSynonyms = sync.OnceValues(func() (*synonyms, error) { return parseSynonyms(synonymsFile) })

func parseSynonyms(data []byte) (*synonyms, error) {
	var file struct {
		Roles   map[string][]string `yaml:",inline"`
		Markers struct {
			Debit  []string `yaml:"debit"`
			Credit []string `yaml:"credit"`
		} `yaml:"direction_markers"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("statements: header_synonyms.yaml: %w", err)
	}
	s := &synonyms{byLabel: map[string]role{}, debits: map[string]bool{}, credits: map[string]bool{}}
	for name, labels := range file.Roles {
		r := role(name)
		for _, label := range labels {
			key := foldLabel(label)
			if previous, ok := s.byLabel[key]; ok && previous != r {
				return nil, fmt.Errorf("statements: header_synonyms.yaml: %q is both %s and %s", label, previous, r)
			}
			s.byLabel[key] = r
			s.labels = append(s.labels, labelRole{key, r})
		}
	}
	for _, m := range file.Markers.Debit {
		s.debits[foldLabel(m)] = true
	}
	for _, m := range file.Markers.Credit {
		s.credits[foldLabel(m)] = true
	}
	return s, nil
}

// match is the role a header names: exactly (strength 2), or by holding a
// label as whole words (strength 1); strength 0 is no role.
func (s *synonyms) match(header string) (role, int) {
	key := foldLabel(header)
	if key == "" {
		return "", 0
	}
	if r, ok := s.byLabel[key]; ok {
		return r, 2
	}
	// Of the labels inside the header, the one that ends last wins, the
	// longer on a tie: in "Betrag Soll" the column is the debit.
	padded := " " + key + " "
	var found role
	end, length := -1, 0
	for _, lr := range s.labels {
		if len(lr.label) <= 2 {
			continue
		}
		at := strings.LastIndex(padded, " "+lr.label+" ")
		if at < 0 {
			continue
		}
		if e := at + len(lr.label); e > end || (e == end && len(lr.label) > length) {
			found, end, length = lr.role, e, len(lr.label)
		}
	}
	if found == "" {
		return "", 0
	}
	return found, 1
}

// directionMarker reads a debit/credit marker cell.
func directionMarker(cell string) (Direction, bool) {
	s, err := loadSynonyms()
	if err != nil {
		return "", false
	}
	key := foldLabel(cell)
	if key == "" {
		key = strings.TrimSpace(cell) // "-" and "+" fold to nothing
	}
	switch {
	case s.debits[key]:
		return Debit, true
	case s.credits[key]:
		return Credit, true
	}
	return "", false
}

// foldLabel lower-cases a label, drops accents and makes everything but
// letters and digits a single space.
func foldLabel(label string) string {
	decomposed := norm.NFD.String(strings.ToLower(label))
	var b strings.Builder
	space := false
	for _, r := range decomposed {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}
