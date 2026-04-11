package pipeline

import (
	"context"
	"fmt"
)

// RetryAdvance re-runs the current pipeline stage for an entry, injecting
// the steward's diagnostic feedback into the prompt. Implements the steward's
// PipelineRetrier interface.
func (p *Pipeline) RetryAdvance(ctx context.Context, entryID, feedback string) error {
	_, err := p.Advance(ctx, AdvanceRequest{
		EntryID:  entryID,
		Action:   ActionAdvance,
		Feedback: feedback,
	})
	if err != nil {
		return fmt.Errorf("retry advance: %w", err)
	}
	return nil
}

// RetryExecute re-runs execution for a specced entry, injecting the steward's
// diagnostic feedback. Implements the steward's PipelineRetrier interface.
func (p *Pipeline) RetryExecute(ctx context.Context, entryID, feedback string) error {
	_, err := p.Execute(ctx, ExecuteRequest{
		EntryID:  entryID,
		Feedback: feedback,
	})
	if err != nil {
		return fmt.Errorf("retry execute: %w", err)
	}
	return nil
}
