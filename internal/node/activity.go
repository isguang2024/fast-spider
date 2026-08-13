package node

func (m *JobManager) HasActiveJobs() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	if len(m.starting) > 0 {
		m.mu.RUnlock()
		return true
	}
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.RUnlock()
	for _, job := range jobs {
		job.mu.Lock()
		terminal := isTerminalJobState(job.state)
		job.mu.Unlock()
		if !terminal {
			return true
		}
	}
	return false
}

func (m *BrowserManager) HasActiveSession() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.launching || m.session != nil
}

func (c *Client) TaskBusyReasons() []string {
	if c == nil {
		return nil
	}
	reasons := make([]string, 0, 4)
	if len(c.requestSem) > 0 {
		reasons = append(reasons, "capability_request")
	}
	if c.jobs != nil && c.jobs.HasActiveJobs() {
		reasons = append(reasons, "job")
	}
	if c.browser != nil && c.browser.HasActiveSession() {
		reasons = append(reasons, "browser")
	}
	if reporter, ok := c.agent.(interface{ BusyForUpdate() bool }); ok && reporter.BusyForUpdate() {
		reasons = append(reasons, "agent")
	}
	return reasons
}

func (c *Client) RuntimeStatus() string {
	if c.ReleaseDraining() || len(c.TaskBusyReasons()) > 0 {
		return "busy"
	}
	return "ready"
}

func (c *Client) BeginReleaseDrain() bool {
	if c == nil {
		return false
	}
	c.activityMu.Lock()
	defer c.activityMu.Unlock()
	if c.releaseDrain || len(c.TaskBusyReasons()) > 0 {
		return false
	}
	c.releaseDrain = true
	return true
}

func (c *Client) EndReleaseDrain() {
	if c == nil {
		return
	}
	c.activityMu.Lock()
	c.releaseDrain = false
	c.activityMu.Unlock()
}

func (c *Client) ReleaseDraining() bool {
	if c == nil {
		return false
	}
	c.activityMu.Lock()
	defer c.activityMu.Unlock()
	return c.releaseDrain
}
