package nodeui

import (
	"context"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

func (a *App) handleReleaseNotice(client *node.Client, pushID, version string) {
	if client == nil || pushID == "" || version == "" {
		return
	}
	a.mu.Lock()
	if !a.config.AutoUpdateEnabled || (a.releasePushRunning && a.releasePushID == pushID) {
		a.mu.Unlock()
		return
	}
	a.releasePushID = pushID
	a.releasePushRunning = true
	a.updateStatus.Pushed = true
	a.updateStatus.LatestVersion = version
	a.mu.Unlock()
	go a.runReleaseNotice(client, pushID, version)
}

func (a *App) runReleaseNotice(client *node.Client, pushID, version string) {
	defer func() {
		a.mu.Lock()
		if a.releasePushID == pushID {
			a.releasePushRunning = false
			a.updateStatus.WaitingForIdle = false
			a.updateStatus.BusyReasons = nil
		}
		a.mu.Unlock()
	}()
	a.mu.Lock()
	parent := a.ctx
	a.mu.Unlock()
	if parent == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	err := a.refreshUpdate(ctx, true)
	cancel()
	if err != nil {
		return
	}
	a.mu.Lock()
	ready := a.updateStatus.Available && a.updateStatus.Ready && a.updateStatus.LatestVersion == version && a.updateArtifact != ""
	a.mu.Unlock()
	if !ready {
		return
	}
	a.waitReleaseIdle(client, pushID)
}

func (a *App) waitReleaseIdle(client *node.Client, pushID string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var idleSince time.Time
	for {
		a.mu.Lock()
		enabled := a.config.AutoUpdateEnabled && a.releasePushID == pushID
		appCtx := a.ctx
		a.mu.Unlock()
		if !enabled || appCtx == nil {
			return
		}
		select {
		case <-appCtx.Done():
			return
		case now := <-ticker.C:
			reasons := client.TaskBusyReasons()
			a.mu.Lock()
			a.updateStatus.WaitingForIdle = true
			a.updateStatus.BusyReasons = append([]string(nil), reasons...)
			a.mu.Unlock()
			if len(reasons) > 0 {
				idleSince = time.Time{}
				continue
			}
			if idleSince.IsZero() {
				idleSince = now
				continue
			}
			if now.Sub(idleSince) < 15*time.Second {
				continue
			}
			if !client.BeginReleaseDrain() {
				idleSince = time.Time{}
				continue
			}
			a.mu.Lock()
			a.updateStatus.WaitingForIdle = false
			a.updateStatus.BusyReasons = nil
			a.mu.Unlock()
			applied, err := a.applyReadyUpdateOnStartup()
			if err != nil || !applied {
				client.EndReleaseDrain()
				if err != nil {
					a.setUpdateError(err)
				}
				return
			}
			a.mu.Lock()
			cancelApp := a.cancel
			a.mu.Unlock()
			if cancelApp != nil {
				go func() {
					time.Sleep(350 * time.Millisecond)
					cancelApp()
				}()
			}
			return
		}
	}
}
