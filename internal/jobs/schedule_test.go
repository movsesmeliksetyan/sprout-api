package jobs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHourly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ current, want string }{
		{"2025-07-21T08:00:00Z", "2025-07-21T08:01:00Z"},
		{"2025-07-21T08:00:59Z", "2025-07-21T08:01:00Z"},
		{"2025-07-21T08:01:00Z", "2025-07-21T09:01:00Z"},
		{"2025-07-21T23:59:00Z", "2025-07-22T00:01:00Z"},
	} {
		current, _ := time.Parse(time.RFC3339, tc.current)
		assert.Equal(t, tc.want, hourly{}.Next(current).Format(time.RFC3339), tc.current)
	}
}
