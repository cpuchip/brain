package pipeline

import (
	"context"
	"fmt"
)

// RetryAdvance re-runs the current pipeline stage for an entry, injecting
// the steward's diagnostic feedback into the prompt. If model is non-empty,
// it overrides the default model for the stage. Implements the steward's
// PipelineRetrier interface.
func (p *Pipeline) RetryAdvance(ctx context.Context, entryID, feedback, model string) error {
	_, err := p.Advance(ctx, AdvanceRequest{
		EntryID:       entryID,
		Action:        ActionAdvance,
		Feedback:      feedback,
		ModelOverride: model,
	})
	if err != nil {
		return fmt.Errorf("retry advance: %w", err)
	}
	return nil
}

// RetryExecute re-runs execution for a specced entry, injecting the steward's
// diagnostic feedback. If model is non-empty, it overrides the default model.
// Implements the steward's PipelineRetrier interface.
func (p *Pipeline) RetryExecute(ctx context.Context, entryID, feedback, model string) error {
	_, err := p.Execute(ctx, ExecuteRequest{
		EntryID:       entryID,
		Feedback:      feedback,
		ModelOverride: model,
	})
	if err != nil {
		return fmt.Errorf("retry execute: %w", err)
	}
	return nil
}
