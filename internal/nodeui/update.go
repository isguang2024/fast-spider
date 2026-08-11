package nodeui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/nodeupdate"
)

type updateStatusResponse struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	Available      bool   `json:"available"`
	Ready          bool   `json:"ready"`
	Checking       bool   `json:"checking"`
	LastCheckedAt  string `json:"lastCheckedAt,omitempty"`
	Error          string `json:"error,omitempty"`
	SizeBytes      int64  `json:"sizeBytes,omitempty"`
}

func (a *App) updateSnapshot() updateStatusResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	status := a.updateStatus
	if status.CurrentVersion == "" {
		status.CurrentVersion = a.opts.Version
	}
	status.Checking = a.updateRunning
	return status
}

func (a *App) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := a.refreshUpdate(ctx, false); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := a.refreshUpdate(ctx, true); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	a.mu.Lock()
	artifact := a.updateArtifact
	available := a.updateStatus.Available && a.updateStatus.Ready
	cancelApp := a.cancel
	a.mu.Unlock()
	if !available || artifact == "" {
		writeJSON(w, http.StatusOK, map[string]any{"restarting": false, "status": a.snapshot()})
		return
	}
	target, err := os.Executable()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	if err := nodeupdate.StartApply(artifact, target, a.opts.DataDir, false); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarting": true})
	if cancelApp != nil {
		go func() {
			time.Sleep(350 * time.Millisecond)
			cancelApp()
		}()
	}
}

func (a *App) refreshUpdate(ctx context.Context, stage bool) error {
	a.mu.Lock()
	if a.updateRunning {
		a.mu.Unlock()
		return errors.New("更新检查正在进行")
	}
	a.updateRunning = true
	a.updateStatus.Checking = true
	a.updateStatus.Error = ""
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.updateRunning = false
		a.updateStatus.Checking = false
		a.mu.Unlock()
	}()

	state, err := node.LoadState(a.opts.DataDir + string(os.PathSeparator) + "state.json")
	if err != nil {
		a.setUpdateError(err)
		return errors.New("请先连接并登记设备，再检查更新")
	}
	status, err := nodeupdate.Check(ctx, state.HubURL, state.HubPublicKey, a.opts.Version)
	if err != nil {
		a.setUpdateError(err)
		return err
	}
	artifact := ""
	if stage && status.Available {
		status, artifact, err = nodeupdate.Stage(ctx, a.opts.DataDir, state.HubURL, state.HubPublicKey, a.opts.Version, status)
		if err != nil {
			a.setUpdateError(err)
			return err
		}
	}
	a.mu.Lock()
	a.updateStatus = updateStatusResponse{
		CurrentVersion: status.CurrentVersion,
		LatestVersion:  status.LatestVersion,
		Available:      status.Available,
		Ready:          status.Ready,
		LastCheckedAt:  time.Now().UTC().Format(time.RFC3339),
		SizeBytes:      status.SizeBytes,
	}
	if artifact != "" {
		a.updateArtifact = artifact
	}
	a.mu.Unlock()
	return nil
}

func (a *App) setUpdateError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updateStatus.CurrentVersion = a.opts.Version
	a.updateStatus.Error = strings.TrimSpace(err.Error())
	a.updateStatus.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
}

func (a *App) autoUpdateLoop(ctx context.Context) {
	initial := time.NewTimer(30 * time.Second)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return
	case <-initial.C:
	}
	for {
		a.mu.Lock()
		enabled := a.config.AutoUpdateEnabled
		a.mu.Unlock()
		if enabled {
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			_ = a.refreshUpdate(checkCtx, true)
			cancel()
		}
		timer := time.NewTimer(6 * time.Hour)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *App) applyReadyUpdateOnStartup() (bool, error) {
	a.mu.Lock()
	enabled := a.config.AutoUpdateEnabled
	a.mu.Unlock()
	if !enabled {
		return false, nil
	}
	state, err := node.LoadState(a.opts.DataDir + string(os.PathSeparator) + "state.json")
	if err != nil {
		return false, nil
	}
	status, artifact, err := nodeupdate.Ready(a.opts.DataDir, state.HubPublicKey, a.opts.Version)
	if err != nil {
		return false, err
	}
	if !status.Ready || artifact == "" {
		return false, nil
	}
	target, err := os.Executable()
	if err != nil {
		return false, err
	}
	if err := nodeupdate.StartApply(artifact, target, a.opts.DataDir, a.opts.NoOpenWindow); err != nil {
		return false, err
	}
	return true, nil
}

func runStartupUpdateMaintenance(applyReady func() (bool, error), cleanupConsumedCurrent func() error) (bool, error) {
	applied, err := applyReady()
	if err != nil || applied {
		return applied, err
	}
	if err := cleanupConsumedCurrent(); err != nil {
		return false, fmt.Errorf("cleanup consumed current Node update: %w", err)
	}
	return false, nil
}

func cleanupLegacyInstallArtifactsOnStartup(executablePath func() (string, error), cleanup func(string) error) error {
	path, err := executablePath()
	if err != nil {
		return fmt.Errorf("resolve current Node executable: %w", err)
	}
	return cleanup(path)
}
