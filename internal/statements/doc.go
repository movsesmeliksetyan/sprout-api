// Package statements reads bank statements into one canonical structure
// (PRD §4): it detects the container, extracts a table, works out how to
// read its columns and turns its rows into normalised transactions.
// Bank-specific knowledge lives in data, never in code.
package statements
