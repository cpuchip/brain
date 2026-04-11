package steward

import "fmt"

// BuildRetryContext generates feedback text for the retrying agent based on
// the failure diagnosis. This context is injected into the retry prompt so
// the agent knows what went wrong and can adjust its approach.
func BuildRetryContext(diagnosis FailureType, failureReason string, attempt int) string {
	var guidance string

	switch diagnosis {
	case FailureTimeout:
		guidance = `The previous attempt timed out due to inactivity.

Suggestions for this retry:
- Break the work into smaller steps and produce output incrementally
- Read files in targeted ranges instead of reading entire files
- Write progress as you go — don't buffer everything until the end
- If the task is too large for one pass, complete the most important part first and note what remains`

	case FailureTransient:
		guidance = `The previous attempt failed due to a transient error (network, rate limit, or service issue).

The underlying issue has likely resolved. Proceed normally with the same approach.`

	case FailureToolError:
		guidance = `The previous attempt failed because an MCP tool call produced an error.

Suggestions for this retry:
- Check whether the tool exists and is available before calling it
- Verify the arguments are correct (check types, paths, required fields)
- If one tool fails, consider whether a different tool can accomplish the same thing
- If the tool is genuinely broken, document the issue and work around it`

	case FailureModelLimit:
		guidance = `This task has failed multiple times with similar errors, suggesting the current approach may need adjustment.

Suggestions for this retry:
- Simplify the task — focus on the core requirement rather than trying to do everything at once
- Re-read the plan/spec carefully — you may be overcomplicating the implementation
- If stuck on a specific part, skip it and complete what you can, then note what remains`

	default:
		guidance = `The previous attempt failed for an unclear reason. Review the error and try a different approach.`
	}

	return fmt.Sprintf(`**Steward retry context (attempt %d):**

Previous failure: %s

%s`, attempt, failureReason, guidance)
}
