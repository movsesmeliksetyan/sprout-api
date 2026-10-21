package categorize

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

//go:embed data/*.txt data/*.tsv
var data embed.FS

// Merchant is a description reduced to the merchant it names.
type Merchant struct {
	// Key is the merchant in the form used for matching: lower case,
	// letters, digits, "&" and single spaces. Every spelling of one
	// merchant has the same key.
	Key string
	// Display is the merchant's name as the app shows it.
	Display string
}

// Normalize reduces a statement description, or a merchant typed by hand,
// to the merchant it names (§5.1): bank boilerplate, payment processors,
// dates, card fragments, store numbers, reference codes, the place and the
// company form are stripped, then known spellings collapse to one merchant.
// It is pure, and idempotent: normalising Display gives the same Merchant.
// An empty description is the zero Merchant.
func Normalize(description string) Merchant {
	return defaultNormalizer().normalize(description)
}

var defaultNormalizer = sync.OnceValue(func() *normalizer {
	n, err := loadNormalizer(data)
	if err != nil {
		panic(err)
	}
	return n
})

// normalizer holds the data files the steps read.
type normalizer struct {
	// prefixes and processors are longest first.
	prefixes     []string
	processors   []string
	countries    map[string]bool
	regions      map[string]bool
	cities       map[string]bool
	maxCityWords int
	suffixes     map[string]bool
	maxSuffixLen int
	legal        map[string]bool
	aliases      map[string]Merchant
	maxAliasLen  int
}

func loadNormalizer(files fs.FS) (*normalizer, error) {
	n := &normalizer{
		countries: map[string]bool{},
		regions:   map[string]bool{},
		cities:    map[string]bool{},
		suffixes:  map[string]bool{},
		legal:     map[string]bool{},
		aliases:   map[string]Merchant{},
	}
	var err error
	if n.prefixes, err = readLines(files, "data/prefixes.txt"); err != nil {
		return nil, err
	}
	if n.processors, err = readLines(files, "data/processors.txt"); err != nil {
		return nil, err
	}
	for _, list := range [][]string{n.prefixes, n.processors} {
		sort.SliceStable(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	}

	for name, set := range map[string]map[string]bool{"data/countries.txt": n.countries, "data/regions.txt": n.regions} {
		lines, err := readLines(files, name)
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			for _, code := range strings.Fields(line) {
				set[code] = true
			}
		}
	}
	suffixes, err := readLines(files, "data/suffixes.txt")
	if err != nil {
		return nil, err
	}
	for _, suffix := range suffixes {
		n.suffixes[suffix] = true
		n.maxSuffixLen = max(n.maxSuffixLen, len(strings.Fields(suffix)))
	}
	cities, err := readLines(files, "data/cities.txt")
	if err != nil {
		return nil, err
	}
	for _, city := range cities {
		n.cities[city] = true
		n.maxCityWords = max(n.maxCityWords, len(strings.Fields(city)))
	}
	legal, err := readLines(files, "data/legal.txt")
	if err != nil {
		return nil, err
	}
	for _, form := range legal {
		n.legal[form] = true
	}

	aliases, err := readLines(files, "data/aliases.tsv")
	if err != nil {
		return nil, err
	}
	for _, line := range aliases {
		columns := strings.Split(line, "\t")
		if len(columns) != 3 {
			return nil, fmt.Errorf("categorize: aliases.tsv: want 3 tab-separated columns in %q", line)
		}
		merchant := Merchant{Key: columns[0], Display: columns[1]}
		if merchant.Key != key(strings.Fields(merchant.Key)) {
			return nil, fmt.Errorf("categorize: aliases.tsv: key %q is not in key form (lower case, letters, digits, \"&\", single spaces)", merchant.Key)
		}
		spellings := append(strings.Split(columns[2], "|"), strings.ToUpper(merchant.Display))
		for _, spelling := range spellings {
			spelling = strings.Join(strings.Fields(spelling), " ")
			if spelling == "" {
				continue
			}
			if other, taken := n.aliases[spelling]; taken && other != merchant {
				return nil, fmt.Errorf("categorize: aliases.tsv: %q is a spelling of both %q and %q", spelling, other.Key, merchant.Key)
			}
			n.aliases[spelling] = merchant
			n.maxAliasLen = max(n.maxAliasLen, len(strings.Fields(spelling)))
		}
	}
	return n, nil
}

// readLines returns a data file's lines, trimmed, without blank lines and
// "#" comments.
func readLines(files fs.FS, name string) ([]string, error) {
	file, err := files.Open(name)
	if err != nil {
		return nil, fmt.Errorf("categorize: %w", err)
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("categorize: %s: %w", name, err)
	}
	return lines, nil
}

func (n *normalizer) normalize(description string) Merchant {
	cleaned := clean(description)
	if cleaned == "" {
		return Merchant{}
	}
	s := n.stripPrefixes(cleaned)
	s = n.unwrapProcessors(s)
	// A known merchant is known by its spelling before anything else goes,
	// web addresses ("BOOKING.COM") included.
	if merchant, ok := n.alias(strings.Fields(s)); ok {
		return merchant
	}
	tokens := n.stripTail(stripCodes(strings.Fields(s)))
	if len(tokens) == 0 {
		// Only codes: the description names no merchant.
		return Merchant{}
	}
	if merchant, ok := n.alias(tokens); ok {
		return merchant
	}
	return Merchant{Key: key(tokens), Display: display(tokens)}
}

// clean upper-cases the description and collapses its white space. Quotes
// around a name go; an apostrophe inside one stays.
func clean(description string) string {
	upper := strings.Map(func(r rune) rune {
		switch r {
		case '«', '»', '"', '“', '”', '„', '‘':
			return ' '
		case '’':
			return '\''
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ' '
		}
		return unicode.ToUpper(r)
	}, description)
	return strings.Join(strings.Fields(upper), " ")
}

// stripPrefixes removes bank boilerplate from the start, as long as some
// follows: "POS VISA CONTACTLESS TESCO" is "TESCO".
func (n *normalizer) stripPrefixes(s string) string {
	for {
		stripped := false
		for _, prefix := range n.prefixes {
			rest, ok := strings.CutPrefix(s, prefix)
			if !ok || (rest != "" && rest[0] != ' ' && !endsInPunct(prefix)) {
				continue
			}
			rest = strings.TrimLeft(rest, " -:*/.")
			if rest == "" {
				continue
			}
			s, stripped = rest, true
			break
		}
		if !stripped {
			return s
		}
	}
}

func endsInPunct(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsPunct(r)
}

// unwrapProcessors removes card masks, then a processor in front of the
// merchant ("SQ *BLUE BOTTLE" is "BLUE BOTTLE"). Any other "NAME*REST"
// keeps NAME, the merchant, and drops what it sold or a reference
// ("ANTHROPIC* CLAUDE SUB" is "ANTHROPIC"), unless NAME and the next word
// are a known merchant together ("UBER *EATS" is "UBER EATS").
func (n *normalizer) unwrapProcessors(s string) string {
	tokens := strings.Fields(s)
	kept := tokens[:0]
	for _, token := range tokens {
		if !isCardMask(token) {
			kept = append(kept, token)
		}
	}
	s = strings.Join(kept, " ")
	for strings.Contains(s, " *") || strings.Contains(s, "* ") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, " *", "*"), "* ", "*")
	}

	for _, processor := range n.processors {
		if rest, ok := strings.CutPrefix(s, processor); ok && rest != "" {
			s = strings.TrimLeft(rest, " *")
			break
		}
	}

	left, right, found := strings.Cut(s, "*")
	if !found {
		return s
	}
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(strings.ReplaceAll(right, "*", " "))
	if letters(left) < 2 {
		return right
	}
	if word, _, _ := strings.Cut(right, " "); word != "" {
		joined := strings.Fields(left + " " + word)
		if spelling := n.longestAlias(joined); spelling > len(strings.Fields(left)) {
			return left + " " + right
		}
	}
	return left
}

// isCardMask reports whether a token is part of a masked card number:
// "*1234", "XXXX1234", "****", "4111**1234".
func isCardMask(token string) bool {
	stars, xs, digits, other := 0, 0, 0, 0
	for _, r := range token {
		switch {
		case r == '*':
			stars++
		case r == 'X':
			xs++
		case r >= '0' && r <= '9':
			digits++
		default:
			other++
		}
	}
	switch {
	case other > 0:
		return false
	case stars > 0:
		// A lone "*" joins a processor to a merchant ("SQ * BAKERY").
		return digits > 0 || (xs == 0 && stars >= 2)
	case xs >= 3:
		return digits > 0 || xs >= 4
	}
	return false
}

func letters(s string) int {
	count := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			count++
		}
	}
	return count
}

// token is a word of a description, with whether a code was stripped right
// before or after it: a city next to a stripped store number is the store's
// place, not part of the name.
type token struct {
	text                        string
	removedBefore, removedAfter bool
}

// stripCodes removes dates, times, store and terminal numbers, reference
// codes and web addresses. A short number that starts a longer name stays
// ("99 CENTS ONLY"; a longer one is a date or a code, "0714 WHOLE FOODS"),
// and a web address that comes first is the name ("APPLE.COM/BILL ITUNES"
// is "APPLE").
func stripCodes(words []string) []token {
	tokens := make([]token, 0, len(words))
	removed := func() {
		if len(tokens) > 0 {
			tokens[len(tokens)-1].removedAfter = true
		}
	}
	pending := false
	for i := 0; i < len(words); i++ {
		word := words[i]
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		switch {
		case (word == "ON" || word == "AT") && isDateOrTime(next):
			i++
		case word == "REF" || word == "REF:" || word == "REF." || word == "REFERENCE":
			if next != "" {
				i++
			}
		case isDateOrTime(word), isReference(word):
		case isNumber(word) && (i > 0 || next == "" || len(word) > 3):
		case isDomain(word):
			if i > 0 {
				break
			}
			tokens = append(tokens, token{text: host(word), removedBefore: pending})
			pending = false
			continue
		default:
			tokens = append(tokens, token{text: word, removedBefore: pending})
			pending = false
			continue
		}
		removed()
		pending = true
	}
	return tokens
}

// isNumber is a store or terminal number: digits, maybe after "#" or "NO.".
func isNumber(word string) bool {
	word = strings.TrimPrefix(strings.TrimPrefix(word, "#"), "NO.")
	word = strings.Trim(word, ".,:;-")
	if word == "" {
		return false
	}
	for _, r := range word {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isReference is a code mixing letters and digits, six or more long, with
// at least two digits: "2K3XX4", "REF123456". Short ones ("B2B", "MP3") are
// names.
func isReference(word string) bool {
	digits, letters, length := 0, 0, 0
	for _, r := range word {
		length++
		switch {
		case r >= '0' && r <= '9':
			digits++
		case unicode.IsLetter(r):
			letters++
		case r == '-' || r == '/' || r == '_':
		default:
			return false
		}
	}
	return length >= 6 && digits >= 2 && letters > 0
}

var months = map[string]bool{
	"JAN": true, "FEB": true, "MAR": true, "APR": true, "MAY": true, "JUN": true,
	"JUL": true, "AUG": true, "SEP": true, "OCT": true, "NOV": true, "DEC": true,
}

// isDateOrTime is "14-07", "14/07/25", "2025-07-14", "14.07.2025", "14JUL",
// "14JUL25", "17:24" or "17:24:05".
func isDateOrTime(word string) bool {
	word = strings.TrimRight(word, ",.")
	if word == "" {
		return false
	}
	// Day and month name: 14JUL, 14JUL25, 14JUL2025.
	i := 0
	for i < len(word) && i < 2 && word[i] >= '0' && word[i] <= '9' {
		i++
	}
	if i > 0 && len(word) >= i+3 && months[word[i:i+3]] && allDigits(word[i+3:]) && len(word[i+3:]) <= 4 {
		return true
	}
	// Numbers joined by one kind of separator: dates and times.
	for _, sep := range []string{"/", "-", ".", ":"} {
		parts := strings.Split(word, sep)
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		ok := true
		for _, part := range parts {
			if part == "" || len(part) > 4 || !allDigits(part) {
				ok = false
				break
			}
		}
		if ok && (sep != "." || len(parts) == 3) {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

var topLevelDomains = map[string]bool{
	"COM": true, "NET": true, "ORG": true, "IO": true, "CO": true, "UK": true, "DE": true,
	"FR": true, "ES": true, "IT": true, "NL": true, "EU": true, "AM": true, "RU": true,
	"APP": true, "AI": true, "US": true, "ME": true, "TV": true, "GOV": true, "INFO": true,
}

// isDomain is a web address: "AMZN.COM/BILL", "HELP.UBER.COM",
// "WWW.AMAZON.CO.UK".
func isDomain(word string) bool {
	hostname, _, _ := strings.Cut(word, "/")
	labels := strings.Split(hostname, ".")
	if len(labels) < 2 || !topLevelDomains[labels[len(labels)-1]] {
		return false
	}
	for _, label := range labels {
		if len(label) < 2 {
			return false
		}
		for _, r := range label {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' {
				return false
			}
		}
	}
	return true
}

// host is the name in a web address: "WWW.AMAZON.CO.UK" is "AMAZON".
func host(word string) string {
	hostname, _, _ := strings.Cut(word, "/")
	labels := strings.Split(hostname, ".")
	if labels[0] == "WWW" && len(labels) > 2 {
		labels = labels[1:]
	}
	return labels[0]
}

// stripTail removes what follows the name, from the end, as long as a name
// is left: country codes, a region code after a city or a stripped code
// ("BOSTON MA"), known cities ("NANDOS EDINBURGH" is "NANDOS"; a merchant
// named after a city loses it), billing words ("PAYMENTS") and company
// forms ("ACME LTD"). Non-Latin company forms go from the start as well
// ("ООО РОМАШКА").
func (n *normalizer) stripTail(tokens []token) []string {
	for len(tokens) > 1 {
		last := tokens[len(tokens)-1]
		if n.countries[last.text] || n.legal[stripPunct(last.text)] {
			tokens = tokens[:len(tokens)-1]
			continue
		}
		if n.regions[last.text] && (last.removedBefore || n.endsWithCity(tokens[:len(tokens)-1]) > 0) {
			tokens = tokens[:len(tokens)-1]
			continue
		}
		if words := n.endsWithCity(tokens); words > 0 {
			tokens = tokens[:len(tokens)-words]
			continue
		}
		if words := n.endsWithSuffix(tokens); words > 0 {
			tokens = tokens[:len(tokens)-words]
			continue
		}
		break
	}
	for len(tokens) > 1 && !isASCII(tokens[0].text) && n.legal[stripPunct(tokens[0].text)] {
		tokens = tokens[1:]
	}
	words := make([]string, len(tokens))
	for i, t := range tokens {
		words[i] = t.text
	}
	return words
}

// endsWithCity is how many words of a known city the tokens end with,
// leaving at least one word before it; 0 when they end with none.
func (n *normalizer) endsWithCity(tokens []token) int {
	return endsWith(tokens, n.cities, n.maxCityWords)
}

func (n *normalizer) endsWithSuffix(tokens []token) int {
	return endsWith(tokens, n.suffixes, n.maxSuffixLen)
}

func endsWith(tokens []token, set map[string]bool, maxWords int) int {
	for words := min(maxWords, len(tokens)-1); words > 0; words-- {
		tail := tokens[len(tokens)-words:]
		texts := make([]string, len(tail))
		for i, t := range tail {
			texts[i] = t.text
		}
		if set[strings.Join(texts, " ")] {
			return words
		}
	}
	return 0
}

func stripPunct(word string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return -1
		}
		return r
	}, word)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// alias is the known merchant the words start with, the longest spelling
// first.
func (n *normalizer) alias(words []string) (Merchant, bool) {
	for length := min(n.maxAliasLen, len(words)); length > 0; length-- {
		if merchant, ok := n.aliases[strings.Join(words[:length], " ")]; ok {
			return merchant, true
		}
	}
	return Merchant{}, false
}

// longestAlias is how many of the words the longest matching spelling
// covers; 0 when none matches.
func (n *normalizer) longestAlias(words []string) int {
	for length := min(n.maxAliasLen, len(words)); length > 0; length-- {
		if _, ok := n.aliases[strings.Join(words[:length], " ")]; ok {
			return length
		}
	}
	return 0
}

// key is the words in matching form: lower case, only letters, digits and
// "&", one space between words.
func key(words []string) string {
	parts := make([]string, 0, len(words))
	for _, word := range words {
		part := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '&' {
				return unicode.ToLower(r)
			}
			return -1
		}, word)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// display is the words title-cased, keeping short consonant-only words,
// initialisms and words with "&" in capitals: "BP", "KFC", "B.Q.", "H&M".
func display(words []string) string {
	out := make([]string, len(words))
	for i, word := range words {
		if keepCapitals(word) {
			out[i] = word
			continue
		}
		runes := []rune(word)
		var b strings.Builder
		for j, r := range runes {
			if !unicode.IsLetter(r) {
				b.WriteRune(r)
				continue
			}
			if j > 0 && unicode.IsLetter(runes[j-1]) {
				b.WriteRune(unicode.ToLower(r))
				continue
			}
			// A word starts here. After an apostrophe ("SAINSBURY'S") or
			// a hyphen before a single letter (an Armenian "-Ի") it is a
			// suffix, not a word.
			suffix := j > 0 && (runes[j-1] == '\'' ||
				(runes[j-1] == '-' && (j+1 == len(runes) || !unicode.IsLetter(runes[j+1]))))
			if suffix {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(unicode.ToUpper(r))
			}
		}
		out[i] = b.String()
	}
	return strings.Join(out, " ")
}

func keepCapitals(word string) bool {
	if strings.ContainsAny(word, "&.") && letters(word) <= 4 {
		return true
	}
	if utf8.RuneCountInString(word) > 3 || !isASCII(word) {
		return false
	}
	for _, r := range word {
		if strings.ContainsRune("AEIOUY", r) || !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}
