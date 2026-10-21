package categorize

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// Promotion thresholds (PRD §5.3).
const (
	// promoteVoters: a key is promoted when at least this many users agree
	// on its type and they are at least 80 % of its voters.
	promoteVoters = 5
	// demoteVoters: a learned or llm row is demoted when under 60 % of at
	// least this many voters agree with it. Fewer voters do not outweigh
	// the row.
	demoteVoters = 3
)

// Learn files the merchant under the category for this user: their own
// rule, which wins over every global layer from the next classification
// on. It has the shape of transactions.MerchantRuleHook, so it runs in the
// transaction that saves the transaction. A category that is not one of
// the user's active ones teaches nothing.
func Learn(ctx context.Context, q db.Querier, userID uuid.UUID, merchantKey string, categoryID uuid.UUID) error {
	if merchantKey == "" {
		return nil
	}
	err := NewStore(q).SaveUserRule(ctx, userID, merchantKey, categoryID, time.Now())
	if errors.Is(err, ErrCategoryNotFound) {
		return nil
	}
	return err
}

// Vote is one row of a committed import: the merchant and the category it
// ended up in.
type Vote struct {
	MerchantKey string
	DisplayName string
	CategoryID  uuid.UUID
}

// RecordVotes records a user's votes from a committed import, in one
// query. Each merchant votes for its category's type; a user has one vote
// a merchant, the latest. Categories without a type cast no vote.
func RecordVotes(ctx context.Context, q db.Querier, userID uuid.UUID, votes []Vote, at time.Time) error {
	if len(votes) == 0 {
		return nil
	}
	params := db.RecordMerchantVotesParams{
		UserID:       userID,
		VotedAt:      at,
		Ids:          make([]uuid.UUID, len(votes)),
		MerchantKeys: make([]string, len(votes)),
		DisplayNames: make([]string, len(votes)),
		CategoryIds:  make([]uuid.UUID, len(votes)),
	}
	for i, vote := range votes {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("categorize: new id: %w", err)
		}
		params.Ids[i] = id
		params.MerchantKeys[i] = vote.MerchantKey
		params.DisplayNames[i] = vote.DisplayName
		params.CategoryIds[i] = vote.CategoryID
	}
	if _, err := q.RecordMerchantVotes(ctx, params); err != nil {
		return fmt.Errorf("categorize: record votes: %w", err)
	}
	return nil
}

// PromoteResult is what a run of PromoteDictionary changed.
type PromoteResult struct {
	// Expired are llm rows deleted for their expiry, Demoted rows deleted
	// for disagreement, Promoted rows written from votes.
	Expired, Demoted, Promoted int
}

// PromoteDictionary recomputes the global dictionary from users' votes
// (PRD §5.3), in one transaction: expired llm rows go, keys that enough
// voters agree on become learned rows, replacing llm rows, and learned and
// llm rows that voters disagree with go. Seed rows are never removed or
// changed in type; one that voters agree with takes their vote count. It is
// what the nightly job runs.
func PromoteDictionary(ctx context.Context, database Database, now time.Time) (PromoteResult, error) {
	var result PromoteResult
	err := db.WithTx(ctx, database, func(tx pgx.Tx) error {
		q := db.New(tx)
		expired, err := q.DeleteExpiredLLMEntries(ctx, now)
		if err != nil {
			return err
		}
		candidates, err := q.ListPromotionCandidates(ctx, promoteVoters)
		if err != nil {
			return err
		}
		result.Expired = int(expired)
		if err := promote(ctx, q, candidates, &result); err != nil {
			return err
		}
		// After promotion, so that a row replaced by votes is not counted
		// as demoted too.
		demoted, err := q.DemoteDictionaryEntries(ctx, demoteVoters)
		result.Demoted = int(demoted)
		return err
	})
	if err != nil {
		return PromoteResult{}, fmt.Errorf("categorize: promote dictionary: %w", err)
	}
	return result, nil
}

// promote writes the candidates as learned rows, in one query.
func promote(ctx context.Context, q db.Querier, candidates []db.ListPromotionCandidatesRow, result *PromoteResult) error {
	if len(candidates) == 0 {
		return nil
	}
	params := db.PromoteDictionaryEntriesParams{
		Ids:           make([]uuid.UUID, len(candidates)),
		MerchantKeys:  make([]string, len(candidates)),
		CategoryTypes: make([]string, len(candidates)),
		DisplayNames:  make([]string, len(candidates)),
		Votes:         make([]int32, len(candidates)),
	}
	for i, c := range candidates {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		params.Ids[i] = id
		params.MerchantKeys[i] = c.MerchantKey
		params.CategoryTypes[i] = c.CategoryType
		params.DisplayNames[i] = c.DisplayName
		params.Votes[i] = c.Votes
	}
	promoted, err := q.PromoteDictionaryEntries(ctx, params)
	result.Promoted = int(promoted)
	return err
}
