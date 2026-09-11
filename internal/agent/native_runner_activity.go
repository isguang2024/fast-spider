package agent

// Business acceptance, active execution, and callback retirement are separate
// facts. A historical ACK or an unanswered question is not running business work.
func nativeProjectActivity(p nativeRunnerProject, tasks []nativeRunnerTask) (string, bool) {
	count, accepted := 0, 0
	running, checking, planning, queued := false, false, false, false
	recovering, awaitingReview := false, false
	for _, t := range tasks {
		needsRecovery := (nativeHolds(t) && t.Recovery != nil && (t.Recovery.Phase == "uncertain" || t.Recovery.Phase == "continuing")) || (t.State == "returned" && t.Result != nil && (t.Result.Outcome == "blocked" || t.Result.ErrorCode != ""))
		recovering = recovering || needsRecovery
		if t.Kind == "planner" {
			planning = planning || (!needsRecovery && (nativeHolds(t) || t.State == "queued" || t.State == "returned"))
			continue
		}
		if t.State == "cancelled" {
			continue
		}
		count++
		if t.State == "accepted" && t.AcceptedVersion == p.GoalVersion {
			accepted++
		}
		running = running || (nativeHolds(t) && !needsRecovery)
		checking = checking || nativeChecking(t)
		awaitingReview = awaitingReview || (t.State == "returned" && !needsRecovery && !nativeChecking(t))
		queued = queued || t.State == "queued" || t.State == "pending_plan"
	}
	businessComplete := count > 0 && count == accepted
	switch {
	case p.CompleteVersion != "" && p.CompleteVersion == p.GoalVersion:
		return "completed", businessComplete
	case running:
		return "running", businessComplete
	case checking:
		return "checking", businessComplete
	case recovering:
		return "needs_recovery", businessComplete
	case len(p.Questions) > 0 && !p.QuestionReviewPending:
		return "waiting_input", businessComplete
	case planning:
		return "planning", businessComplete
	case awaitingReview:
		return "awaiting_review", businessComplete
	case queued:
		return "queued", businessComplete
	default:
		return "idle", businessComplete
	}
}
