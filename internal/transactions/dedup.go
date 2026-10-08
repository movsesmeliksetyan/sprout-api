package transactions

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// MerchantKey returns merchant in the form used for matching: the same key
// for every spelling of one merchant. Until the normaliser exists it only
// trims and lower-cases.
func MerchantKey(merchant string) string {
	return strings.ToLower(strings.TrimSpace(merchant))
}

// DedupHash identifies what a statement row is compared with to find
// duplicates: the SHA-256 of the local date, the amount, the direction and
// the merchant key. Statement import computes the same hash for its rows.
func DedupHash(localDate time.Time, amountMinor int64, kind Kind, merchantKey string) []byte {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%s|%s",
		localDate.Format(time.DateOnly), amountMinor, kind, merchantKey))
	return sum[:]
}
