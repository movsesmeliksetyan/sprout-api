package transactions

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/categorize"
)

// MerchantKey returns merchant in the form used for matching: the same key
// for every spelling of one merchant, typed by hand or read from a
// statement (categorize.Normalize). It is empty for a merchant that is only
// codes.
func MerchantKey(merchant string) string {
	return categorize.Normalize(merchant).Key
}

// DedupHash identifies what a statement row is compared with to find
// duplicates: the SHA-256 of the local date, the amount, the direction and
// the merchant key. Statement import computes the same hash for its rows.
func DedupHash(localDate time.Time, amountMinor int64, kind Kind, merchantKey string) []byte {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%s|%s",
		localDate.Format(time.DateOnly), amountMinor, kind, merchantKey))
	return sum[:]
}
