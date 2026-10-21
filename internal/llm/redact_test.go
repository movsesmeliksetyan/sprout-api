package llm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

func TestRedact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		// Emails.
		{"email", "Refund to jane.doe+shop@example.co.uk today", "Refund to [EMAIL] today"},
		{"email in a reference", "PAYPAL *SHOP REF a.b@mail.example.com", "PAYPAL *SHOP REF [EMAIL]"},

		// IBANs, which pass the mod-97 check.
		{"IBAN", "TRANSFER TO GB82WEST12345698765432", "TRANSFER TO [IBAN]"},
		{"IBAN in groups", "IBAN DE89 3704 0044 0532 0130 00 SALARY", "IBAN [IBAN] SALARY"},
		{"IBAN in lower case", "to gb82west12345698765432", "to [IBAN]"},
		{"IBAN followed by words", "FROM GB82 WEST 1234 5698 7654 32 RENT JULY", "FROM [IBAN] RENT JULY"},
		{"not an IBAN: the check fails", "REF GB00WEST12345698765432", "REF GB00WEST12345698765432"},

		// Card numbers: 13 to 19 digits that pass the Luhn check.
		{"card", "CARD 4111111111111111 TESCO", "CARD [CARD] TESCO"},
		{"card in groups", "card 5555 5555 5555 4444 used", "card [CARD] used"},
		{"card with dashes", "4012-8888-8888-1881", "[CARD]"},
		{"a masked card stays", "CARD ****1234 TESCO", "CARD ****1234 TESCO"},
		{"a card fragment stays", "XXXX1234 SHELL", "XXXX1234 SHELL"},

		// Account numbers: other runs of eight digits or more.
		{"account number", "STANDING ORDER 12345678 SORT 12-34-56", "STANDING ORDER [ACCOUNT] SORT 12-34-56"},
		{"account number longer than a card", "Ameriabank 15700123456789012345", "Ameriabank [ACCOUNT]"},
		{"card-length run that fails Luhn", "ACC 4111111111111112", "ACC [ACCOUNT]"},
		{"phone number", "Call +374 10 123456", "Call +[ACCOUNT]"},

		// What a statement needs stays.
		{"amount", "-3,224.00 AMD", "-3,224.00 AMD"},
		{"amount in minor units", "1234567", "1234567"},
		{"date YYYYMMDD", "20250715", "20250715"},
		{"date DDMMYYYY", "15072025", "15072025"},
		{"date with separators", "30/09/26, 14:40", "30/09/26, 14:40"},
		{"terminal code", "Ք: BAR B.Q. YEREVAN AM 629529", "Ք: BAR B.Q. YEREVAN AM 629529"},
		{"store number", "TESCO STORES 3297 LONDON GB", "TESCO STORES 3297 LONDON GB"},
		{"digits inside a word", "ID 01a11bd15c467dfe", "ID 01a11bd15c467dfe"},
		{"nothing to mask", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, llm.Redact(tt.in))
		})
	}
}

func TestRedact_IsIdempotent(t *testing.T) {
	t.Parallel()
	in := "jane@example.com, GB82WEST12345698765432, 4111111111111111, 12345678"
	once := llm.Redact(in)
	assert.Equal(t, "[EMAIL], [IBAN], [CARD], [ACCOUNT]", once)
	assert.Equal(t, once, llm.Redact(once))
}
