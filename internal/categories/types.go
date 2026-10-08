package categories

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"fmt"
	"strings"
	"unicode"
)

// typesCSV maps category names and icons to canonical category types. The
// knowledge lives in the file; this code only reads it.
//
//go:embed data/types.csv
var typesCSV []byte

// typeHints is typesCSV, indexed.
type typeHints struct {
	byName map[string]string
	byIcon map[string]string
}

var hints = mustLoadTypeHints(typesCSV)

func mustLoadTypeHints(data []byte) typeHints {
	loaded, err := loadTypeHints(data)
	if err != nil {
		panic(err)
	}
	return loaded
}

func loadTypeHints(data []byte) (typeHints, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = 3
	records, err := reader.ReadAll()
	if err != nil {
		return typeHints{}, fmt.Errorf("categories: read types.csv: %w", err)
	}

	loaded := typeHints{byName: map[string]string{}, byIcon: map[string]string{}}
	for i, record := range records {
		if i == 0 {
			continue // header
		}
		match, value, categoryType := record[0], record[1], record[2]
		switch match {
		case "name":
			loaded.byName[normaliseName(value)] = categoryType
		case "icon":
			loaded.byIcon[value] = categoryType
		default:
			return typeHints{}, fmt.Errorf("categories: types.csv line %d: unknown match %q", i+1, match)
		}
	}
	return loaded, nil
}

// InferType guesses the canonical type of a custom category, or returns nil
// when nothing fits. The name decides first, as a whole and then word by
// word; the icon is the fallback.
func InferType(name, icon string) *string {
	normalised := normaliseName(name)
	if categoryType, ok := hints.byName[normalised]; ok {
		return &categoryType
	}
	for _, word := range strings.Fields(normalised) {
		if categoryType, ok := hints.byName[word]; ok {
			return &categoryType
		}
	}
	if categoryType, ok := hints.byIcon[icon]; ok {
		return &categoryType
	}
	return nil
}

// normaliseName lower-cases name and reduces everything between its words
// to one space, so "Eating  Out" and "eating-out" compare equal.
func normaliseName(name string) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, " ")
}
