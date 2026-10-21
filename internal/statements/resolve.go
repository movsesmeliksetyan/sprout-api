package statements

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/llm"
)

// MappingSource says how a mapping was found (contract §2.10).
type MappingSource string

// The mapping sources.
const (
	SourceTemplate  MappingSource = "template"
	SourceHeuristic MappingSource = "heuristic"
	SourceLLM       MappingSource = "llm"
	SourceUser      MappingSource = "user"
	SourceNative    MappingSource = "native"
)

// checkSamples is how many rows a spec is checked against.
const checkSamples = 50

// ErrNoMapping is a table no stage could find a mapping for: the import
// needs the user to say how to read it.
var ErrNoMapping = errors.New("statements: no mapping found")

// Resolution is the mapping found for a table.
type Resolution struct {
	Spec        MappingSpec
	Source      MappingSource
	Fingerprint string
	// Confidence is the heuristic's, in percent; 100 for a template.
	Confidence int
	BankHint   *string
}

// HeuristicFunc proposes a mapping and its confidence, as Heuristic does.
type HeuristicFunc func(RawTable, Hints) (MappingSpec, int)

// Resolver finds how to read a table (PRD §4.3 stage 3): a template saved
// for its headers, else the heuristics, else the model. A spec found is
// saved as a template, so the next file with the same headers needs
// neither.
type Resolver struct {
	q         db.Querier
	heuristic HeuristicFunc
	llm       llm.Client
}

// ResolverOption configures a Resolver.
type ResolverOption func(*Resolver)

// WithHeuristic replaces Heuristic.
func WithHeuristic(fn HeuristicFunc) ResolverOption {
	return func(r *Resolver) { r.heuristic = fn }
}

// WithLLM lets the resolver ask the model when the heuristics are not sure
// (PRD §4.3 stage 3c).
func WithLLM(client llm.Client) ResolverOption {
	return func(r *Resolver) { r.llm = client }
}

// NewResolver returns a resolver keeping its templates through q.
func NewResolver(q db.Querier, opts ...ResolverOption) *Resolver {
	r := &Resolver{q: q, heuristic: Heuristic}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Fingerprint identifies a header layout: the SHA-256 of its labels, lower
// case, trimmed and without accents, in order.
func Fingerprint(headers []string) string {
	h := sha256.New()
	for _, header := range headers {
		h.Write([]byte(foldLabel(header)))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Resolve finds a mapping for a table of format. ErrNoMapping says no
// stage could read it: the user is asked (NewMappingRequest).
func (r *Resolver) Resolve(ctx context.Context, table RawTable, format Format, hints Hints) (Resolution, error) {
	fingerprint := Fingerprint(table.Headers)
	template, err := r.q.GetMappingTemplate(ctx, fingerprint)
	switch {
	case err == nil:
		var spec MappingSpec
		if json.Unmarshal(template.Spec, &spec) == nil && spec.Check(table, checkSamples) == nil {
			return Resolution{Spec: spec, Source: SourceTemplate, Fingerprint: fingerprint, Confidence: 100, BankHint: template.BankHint}, nil
		}
		// A template that does not read this file falls through.
	case !errors.Is(err, pgx.ErrNoRows):
		return Resolution{}, fmt.Errorf("statements: template: %w", err)
	}

	spec, confidence := r.heuristic(table, hints)
	if confidence >= HeuristicThreshold && spec.Check(table, checkSamples) == nil {
		if err := r.Save(ctx, fingerprint, format, nil, spec, SourceHeuristic); err != nil {
			return Resolution{}, err
		}
		return Resolution{Spec: spec, Source: SourceHeuristic, Fingerprint: fingerprint, Confidence: confidence}, nil
	}

	if r.llm != nil {
		// A failure or a mapping that does not read the file leaves the
		// user to say how to read it; it never fails the import.
		if spec, err := InferMapping(ctx, r.llm, table); err == nil {
			if err := r.Save(ctx, fingerprint, format, nil, spec, SourceLLM); err != nil {
				return Resolution{}, err
			}
			return Resolution{Spec: spec, Source: SourceLLM, Fingerprint: fingerprint, Confidence: 100}, nil
		}
	}
	return Resolution{Fingerprint: fingerprint}, ErrNoMapping
}

// Save stores a validated spec as the template for a fingerprint. A user's
// spec replaces any other; any other replaces neither a user's nor a
// seeded one, unless that template has been disabled.
func (r *Resolver) Save(ctx context.Context, fingerprint string, format Format, bankHint *string, spec MappingSpec, source MappingSource) error {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("statements: template: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("statements: new id: %w", err)
	}
	if err := r.q.SaveMappingTemplate(ctx, db.SaveMappingTemplateParams{
		ID: id, HeaderFingerprint: fingerprint, Format: string(format), BankHint: bankHint, Spec: encoded, Source: string(source),
	}); err != nil {
		return fmt.Errorf("statements: save template: %w", err)
	}
	return nil
}

// RecordUse counts an import read with the fingerprint's template, and
// whether it went well (PRD §4.4); a template that does badly is disabled.
func (r *Resolver) RecordUse(ctx context.Context, fingerprint string, succeeded bool) error {
	if err := r.q.RecordMappingTemplateUse(ctx, db.RecordMappingTemplateUseParams{HeaderFingerprint: fingerprint, Succeeded: succeeded}); err != nil {
		return fmt.Errorf("statements: template use: %w", err)
	}
	return nil
}
