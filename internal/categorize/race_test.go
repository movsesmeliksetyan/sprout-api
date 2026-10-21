//go:build race

package categorize_test

// raceEnabled: the race detector slows code down several times, which
// makes timing tests meaningless.
const raceEnabled = true
