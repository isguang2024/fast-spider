package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"
)

const defaultNativeRunnerGlobalConcurrency = 8

type nativeRunnerSettings struct {
	GlobalConcurrency int `json:"globalConcurrency"`
}

type nativeRunnerSchedulingSnapshot struct {
	QueueReasons map[string]*nativeRunnerQueueReason    `json:"queueReasons,omitempty"`
	GlobalLimit  int                                    `json:"globalLimit"`
	GlobalActive int                                    `json:"globalActive"`
	Allocations  map[string]int                         `json:"allocations"`
	Demands      map[string]int                         `json:"demands"`
	Projects     map[string]nativeRunnerProjectSchedule `json:"projects"`
	Selected     map[string]bool                        `json:"-"`
}

type nativeRunnerProjectSchedule struct {
	ProjectLimit          int `json:"projectLimit"`
	ProjectActive         int `json:"projectActive"`
	ProjectBusinessActive int `json:"projectBusinessActive"`
	Allocation            int `json:"allocation"`
	Demand                int `json:"demand"`
}

// nativeLoadSchedulingSnapshot reads one committed ledger view for status
// responses. The allocation itself stays in nativeBuildSchedulingSnapshot so
// status, UI and dispatch cannot drift into separate scheduling rules.
func nativeLoadSchedulingSnapshot(ctx context.Context, tx *sql.Tx, limit int) (nativeRunnerSchedulingSnapshot, error) {
	if limit < 1 {
		configured, err := nativeLoadGlobalConcurrency(tx)
		if err != nil {
			return nativeRunnerSchedulingSnapshot{}, err
		}
		limit = configured
	}
	projects := []nativeRunnerProject{}
	rows, err := tx.QueryContext(ctx, "SELECT value FROM runner_projects ORDER BY rowid")
	if err != nil {
		return nativeRunnerSchedulingSnapshot{}, err
	}
	for rows.Next() {
		var raw string
		var project nativeRunnerProject
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &project)
		}
		if err != nil {
			rows.Close()
			return nativeRunnerSchedulingSnapshot{}, err
		}
		projects = append(projects, project)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nativeRunnerSchedulingSnapshot{}, err
	}
	rows.Close()

	tasks := []nativeRunnerTask{}
	rows, err = tx.QueryContext(ctx, "SELECT value FROM runner_tasks ORDER BY rowid")
	if err != nil {
		return nativeRunnerSchedulingSnapshot{}, err
	}
	for rows.Next() {
		var raw string
		var task nativeRunnerTask
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &task)
		}
		if err != nil {
			rows.Close()
			return nativeRunnerSchedulingSnapshot{}, err
		}
		tasks = append(tasks, task)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nativeRunnerSchedulingSnapshot{}, err
	}
	rows.Close()
	return nativeBuildSchedulingSnapshot(tasks, projects, limit), nil
}

type nativeRunnerQueryRow interface {
	QueryRow(string, ...any) *sql.Row
}

func nativeLoadGlobalConcurrency(db nativeRunnerQueryRow) (int, error) {
	var raw string
	err := db.QueryRow("SELECT value FROM runner_settings WHERE id='global'").Scan(&raw)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var settings nativeRunnerSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return 0, err
	}
	if settings.GlobalConcurrency < 1 {
		return 0, nil
	}
	return settings.GlobalConcurrency, nil
}

func nativeSaveGlobalConcurrency(tx *sql.Tx, limit int) error {
	raw, err := json.Marshal(nativeRunnerSettings{GlobalConcurrency: limit})
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO runner_settings(id,value) VALUES('global',?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", string(raw))
	return err
}

func nativeRunnerTaskWeight(task nativeRunnerTask) int {
	if task.EstimatedMinutes > 10 {
		return 2
	}
	return 1
}

func nativeRunnerTaskBefore(a, b nativeRunnerTask) bool {
	if (a.Kind == "planner") != (b.Kind == "planner") {
		return a.Kind == "planner"
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if a.DispatchOrdinal != b.DispatchOrdinal {
		return a.DispatchOrdinal < b.DispatchOrdinal
	}
	return a.ID < b.ID
}

// nativeBuildSchedulingSnapshot computes ready-demand allocation for both the
// read-only view and scheduler, including current retry deadlines.
func nativeBuildSchedulingSnapshot(tasks []nativeRunnerTask, projects []nativeRunnerProject, limit int, clock ...time.Time) nativeRunnerSchedulingSnapshot {
	if limit < 1 {
		limit = defaultNativeRunnerGlobalConcurrency
	}
	snapshot := nativeRunnerSchedulingSnapshot{GlobalLimit: limit, Allocations: map[string]int{}, Demands: map[string]int{}, Projects: map[string]nativeRunnerProjectSchedule{}, Selected: map[string]bool{}}
	for _, task := range tasks {
		if nativeHolds(task) {
			snapshot.GlobalActive++
		}
	}

	type demand struct {
		project     nativeRunnerProject
		tasks       []nativeRunnerTask
		served      int
		cursor      int
		latest      int64
		longRunning bool
	}
	demands := make([]demand, 0, len(projects))
	accepted := map[string]bool{}
	latestByProject := map[string]int64{}
	maxOrdinal := int64(0)
	for _, task := range tasks {
		accepted[task.ID] = task.State == "accepted"
		if task.DispatchOrdinal > latestByProject[task.ProjectID] {
			latestByProject[task.ProjectID] = task.DispatchOrdinal
		}
		if task.DispatchOrdinal > maxOrdinal {
			maxOrdinal = task.DispatchOrdinal
		}
	}
	holds := make([]nativeRunnerTask, 0)
	for _, task := range tasks {
		if nativeHolds(task) || nativeChecking(task) || task.State == "integrating" || task.State == "awaiting_integration" {
			holds = append(holds, task)
		}
	}
	now := time.Now().Unix()
	if len(clock) > 0 {
		now = clock[0].Unix()
	}
	for _, project := range projects {
		projectActive := 0
		projectBusinessActive := 0
		longRunning := false
		for _, task := range tasks {
			if task.ProjectID == project.ID && nativeHolds(task) {
				projectActive++
				if task.Kind != "planner" && task.StartedAt > 0 && now-task.StartedAt > 10*60 {
					longRunning = true
				}
				if task.Kind != "planner" {
					projectBusinessActive++
				}
			}
		}
		projectLimit := project.MaxConcurrency
		snapshot.Projects[project.ID] = nativeRunnerProjectSchedule{ProjectLimit: projectLimit, ProjectActive: projectActive, ProjectBusinessActive: projectBusinessActive}
		if project.Archived || project.Paused || project.State == "canceling" || project.State == "cancelled" {
			continue
		}
		items := []nativeRunnerTask{}
		for _, task := range tasks {
			if task.State == "workspace_preparing" || task.ProjectID != project.ID || task.Kind == "" || task.Archived || task.Result != nil || task.Request != nil || task.State == "accepted" || task.State == "deferred" || task.State == "pending_plan" || task.State == "cancelled" || task.State == "canceling" || task.GoalVersion != project.GoalVersion || (task.PlanRevision > 0 && task.PlanRevision < project.Revision) || task.NextAt > now {
				continue
			}
			ready := len(nativeRequirementsMissing(task, tasks)) == 0
			for _, dependency := range task.After {
				if !accepted[dependency] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			if task.LastError != "" && task.NextAt == 0 {
				continue
			}
			if len(task.History) > 0 && !task.Rotate && nativeHistoryBlocksDispatch(task.History[len(task.History)-1]) {
				continue
			}
			for _, held := range holds {
				if nativeTaskScopeConflict(task, held) {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			items = append(items, task)
		}
		sort.SliceStable(items, func(i, j int) bool {
			return nativeRunnerTaskBefore(items[i], items[j])
		})
		if len(items) > 0 {
			demands = append(demands, demand{project: project, tasks: items, latest: latestByProject[project.ID], longRunning: longRunning})
			snapshot.Projects[project.ID] = nativeRunnerProjectSchedule{ProjectLimit: projectLimit, ProjectActive: projectActive, ProjectBusinessActive: projectBusinessActive, Demand: len(items)}
		}
	}
	for _, item := range demands {
		snapshot.Demands[item.project.ID] = len(item.tasks)
	}
	free := limit - snapshot.GlobalActive
	selected := []nativeRunnerTask{}
	for free > 0 {
		best := -1
		bestScore := -1.0
		bestLatest := int64(0)
		bestID := ""
		for i := range demands {
			for demands[i].cursor < len(demands[i].tasks) {
				candidate := demands[i].tasks[demands[i].cursor]
				blocked := false
				for _, prior := range selected {
					if nativeTaskScopeConflict(candidate, prior) {
						blocked = true
						break
					}
				}
				if !blocked {
					break
				}
				demands[i].cursor++
			}
			if demands[i].cursor >= len(demands[i].tasks) {
				continue
			}
			next := demands[i].tasks[demands[i].cursor]
			if demands[i].project.MaxConcurrency > 0 {
				active := snapshot.Projects[demands[i].project.ID].ProjectActive + demands[i].served
				if active >= demands[i].project.MaxConcurrency {
					continue
				}
			}
			weight := nativeRunnerTaskWeight(next)
			if next.Kind != "planner" && next.EstimatedMinutes == 0 && demands[i].longRunning {
				weight = 2
			}
			age := maxOrdinal - demands[i].latest + 1
			if age < 1 {
				age = 1
			}
			// Weight governs the normal share; accumulated dispatch age prevents
			// repeated short turns in an earlier area from starving other areas.
			score := float64(weight)/float64(snapshot.Projects[demands[i].project.ID].ProjectActive+demands[i].served+1) + float64(age-1)/float64(2*limit)
			if score > bestScore || (score == bestScore && (best < 0 || demands[i].latest < bestLatest || (demands[i].latest == bestLatest && demands[i].project.ID < bestID))) {
				best, bestScore, bestLatest, bestID = i, score, demands[i].latest, demands[i].project.ID
			}
		}
		if best < 0 {
			break
		}
		selected = append(selected, demands[best].tasks[demands[best].cursor])
		snapshot.Selected[demands[best].tasks[demands[best].cursor].ID] = true
		demands[best].cursor++
		demands[best].served++
		maxOrdinal++
		demands[best].latest = maxOrdinal
		snapshot.Allocations[demands[best].project.ID]++
		projectSchedule := snapshot.Projects[demands[best].project.ID]
		projectSchedule.Allocation++
		snapshot.Projects[demands[best].project.ID] = projectSchedule
		free--
	}
	snapshot.QueueReasons = map[string]*nativeRunnerQueueReason{}
	for _, project := range projects {
		for _, task := range tasks {
			if task.ProjectID == project.ID {
				snapshot.QueueReasons[task.ID] = nativeQueueReason(task, project, tasks, snapshot, now)
			}
		}
	}
	return snapshot
}
