package nodeui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/isguang2024/fast-spider/internal/browserext"
)

type browserExtensionView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	ManifestVersion int    `json:"manifestVersion"`
	InstalledAt     string `json:"installedAt"`
}

type browserExtensionsResponse struct {
	Extensions []browserExtensionView `json:"extensions"`
}

type browserExtensionInstallRequest struct {
	ArchivePath string `json:"archivePath"`
}

func (a *App) handleBrowserExtensions(w http.ResponseWriter, _ *http.Request) {
	installed, err := browserext.List(a.opts.DataDir)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("读取已安装浏览器插件失败"))
		return
	}
	views := make([]browserExtensionView, 0, len(installed))
	for _, extension := range installed {
		views = append(views, browserExtensionView{
			ID: extension.ID, Name: extension.Name, Version: extension.Version,
			ManifestVersion: extension.ManifestVersion, InstalledAt: extension.InstalledAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, browserExtensionsResponse{Extensions: views})
}

func (a *App) handleBrowserExtensionInstall(w http.ResponseWriter, r *http.Request) {
	var req browserExtensionInstallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, errors.New("浏览器插件安装请求无效"))
		return
	}
	req.ArchivePath = strings.TrimSpace(req.ArchivePath)
	if req.ArchivePath == "" || len(req.ArchivePath) > 4096 {
		writeAPIError(w, http.StatusBadRequest, errors.New("插件 ZIP 的绝对路径不能为空且不能超过 4096 个字符"))
		return
	}
	installed, err := browserext.InstallArchive(a.opts.DataDir, req.ArchivePath)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, errors.New("插件安装失败，请确认 ZIP 内含 manifest.json 且路径安全"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"extension": browserExtensionView{
			ID: installed.ID, Name: installed.Name, Version: installed.Version,
			ManifestVersion: installed.ManifestVersion, InstalledAt: installed.InstalledAt.Format("2006-01-02T15:04:05Z07:00"),
		},
		"requiresNewBrowserSession": true,
	})
}
