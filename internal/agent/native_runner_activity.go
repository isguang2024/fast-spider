package agent

// Business acceptance, active execution, and callback retirement are separate
// facts. A historical ACK or an unanswered question is not running business work.
func nativeProjectActivity(p nativeRunnerProject, tasks []nativeRunnerTask) (string, bool) {
	count, accepted := 0, 0
	running, checking, planning, queued := false, false, false, false
	for _, t := range tasks {
		if t.Kind == "planner" {
			planning = planning || nativeHolds(t) || t.State == "queued" || t.State == "returned"
			continue
		}
		if t.State == "cancelled" {
			continue
		}
		count++
		if t.State == "accepted" && t.AcceptedVersion == p.GoalVersion {
			accepted++
		}
		running = running || nativeHolds(t)
		checking = checking || nativeChecking(t) || t.State == "returned"
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
	case len(p.Questions) > 0 && !p.QuestionReviewPending:
		return "waiting_input", businessComplete
	case planning:
		return "planning", businessComplete
	case queued:
		return "queued", businessComplete
	default:
		return "idle", businessComplete
	}
}
