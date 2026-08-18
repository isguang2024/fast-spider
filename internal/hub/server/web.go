package server

import (
	"crypto/subtle"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const webSessionCookieName = "fast_spider_session"

//go:embed web/*
var webFiles embed.FS

var webTemplates = map[string]*template.Template{
	"setup":       template.Must(template.ParseFS(webFiles, "web/setup.html")),
	"login":       template.Must(template.ParseFS(webFiles, "web/login.html")),
	"admin-login": template.Must(template.ParseFS(webFiles, "web/admin_login.html")),
	"admin":       template.Must(template.ParseFS(webFiles, "web/admin_users.html")),
	"authorize":   template.Must(template.ParseFS(webFiles, "web/authorize.html")),
	"app":         template.Must(template.ParseFS(webFiles, "web/admin.html")),
	"token":       template.Must(template.ParseFS(webFiles, "web/token.html")),
	"direct-key":  template.Must(template.ParseFS(webFiles, "web/direct-key.html")),
}

type setupPageData struct {
	BasePath    string
	Error       string
	Username    string
	DisplayName string
}

type loginPageData struct {
	BasePath string
	Error    string
	Username string
	ReturnTo string
}

type hiddenField struct {
	Name  string
	Value string
}

type authorizePageData struct {
	BasePath    string
	DisplayName string
	ClientName  string
	ClientID    string
	ScopeLabel  string
	Description string
	CSRFToken   string
	Error       string
	Fields      []hiddenField
}

type machinePageView struct {
	core.MachineView
	LastSeen string
}

type oauthClientPageView struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
	GrantTypes   string
	Scope        string
	CreatedAt    string
}

type oauthAuthorizationPageView struct {
	AuthorizationID string
	ClientID        string
	ClientName      string
	Scopes          string
	CreatedAt       string
	LastUsedAt      string
	Status          string
}

type apiTokenPageView struct {
	ID         string
	Label      string
	CreatedAt  string
	LastUsedAt string
	ExpiresAt  string
	Status     string
}

type directKeyPageView struct {
	ID         string
	Label      string
	Scopes     string
	Machine    string
	RateLimit  int
	CreatedAt  string
	LastUsedAt string
	ExpiresAt  string
	Status     string
}

type tokenPageData struct {
	BasePath string
	Label    string
	Token    string
	Expires  string
}

type directKeyPageData struct {
	BasePath  string
	DirectURL string
	Label     string
	Token     string
	Expires   string
	Scopes    string
	Machine   string
	RateLimit int
}

type appPageData struct {
	Page                 string
	PageTitle            string
	Version              string
	BasePath             string
	BaseURL              string
	MCPURL               string
	DirectURL            string
	Username             string
	DisplayName          string
	CSRFToken            string
	Notice               string
	Error                string
	Machines             []machinePageView
	OnlineMachines       int
	Clients              []oauthClientPageView
	Authorizations       []oauthAuthorizationPageView
	ActiveAuthorizations int
	Tokens               []apiTokenPageView
	ActiveTokens         int
	DirectKeys           []directKeyPageView
	ActiveDirectKeys     int
}

func (s *Server) handleWebRoot(w http.ResponseWriter, r *http.Request) {
	hasOwnerAccount, err := s.service.HasOwnerAccount(r.Context())
	if err != nil {
		http.Error(w, "Fast Spider is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !hasOwnerAccount {
		s.redirectPublic(w, r, "/setup", http.StatusFound)
		return
	}
	if _, err := s.currentWebSession(r); err == nil {
		s.redirectPublic(w, r, "/app", http.StatusFound)
		return
	}
	s.redirectPublic(w, r, "/login", http.StatusFound)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	hasOwnerAccount, err := s.service.HasOwnerAccount(r.Context())
	if err != nil {
		http.Error(w, "Fast Spider is unavailable", http.StatusServiceUnavailable)
		return
	}
	if hasOwnerAccount {
		s.redirectPublic(w, r, "/login", http.StatusFound)
		return
	}
	data := setupPageData{BasePath: s.publicBasePath(r), DisplayName: "Fast Spider Owner"}
	if r.Method == http.MethodGet {
		s.renderWebPage(w, "setup", data)
		return
	}
	if !s.parseWebForm(w, r) {
		return
	}
	data.Username = strings.TrimSpace(r.PostForm.Get("username"))
	data.DisplayName = strings.TrimSpace(r.PostForm.Get("display_name"))
	password := r.PostForm.Get("password")
	if password == "" || password != r.PostForm.Get("password_confirm") {
		data.Error = "两次输入的密码不一致。"
		s.renderWebPage(w, "setup", data)
		return
	}
	account, err := s.service.BootstrapAccount(
		r.Context(),
		r.PostForm.Get("bootstrap_token"),
		data.Username,
		data.DisplayName,
		password,
		remoteIP(r),
	)
	if err != nil {
		data.Error = "无法完成首次设置。请确认设置链接仍有效，用户名格式正确，且密码不少于 10 个字符。"
		s.renderWebPage(w, "setup", data)
		return
	}
	session, err := s.service.CreateWebSession(r.Context(), account.OwnerID)
	if err != nil {
		s.redirectPublic(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setWebSessionCookie(w, r, session.Token, session.Record.ExpiresAt)
	s.redirectPublic(w, r, "/app", http.StatusSeeOther)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	hasOwnerAccount, err := s.service.HasOwnerAccount(r.Context())
	if err != nil {
		http.Error(w, "Fast Spider is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !hasOwnerAccount {
		s.redirectPublic(w, r, "/setup", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		if _, err := s.currentWebSession(r); err == nil {
			s.redirectPublic(w, r, "/app", http.StatusFound)
			return
		}
		s.renderWebPage(w, "login", loginPageData{
			BasePath: s.publicBasePath(r),
			ReturnTo: s.safeReturnTo(r, r.URL.Query().Get("return_to")),
		})
		return
	}
	loginKey := remoteIP(r)
	now := time.Now().UTC()
	if s.loginLimiter.blocked(loginKey, now) {
		w.Header().Set("Retry-After", "900")
		s.renderWebPageStatus(w, "login", loginPageData{
			BasePath: s.publicBasePath(r),
			Error:    "登录失败次数过多，请 15 分钟后再试。",
			ReturnTo: s.publicURL(r, "/app"),
		}, http.StatusTooManyRequests)
		return
	}
	if !s.parseWebForm(w, r) {
		return
	}
	data := loginPageData{
		BasePath: s.publicBasePath(r),
		Username: strings.TrimSpace(r.PostForm.Get("username")),
		ReturnTo: s.safeReturnTo(r, r.PostForm.Get("return_to")),
	}
	account, err := s.service.LoginAccount(r.Context(), data.Username, r.PostForm.Get("password"), loginKey)
	if err != nil {
		s.loginLimiter.failure(loginKey, now)
		data.Error = "用户名或密码不正确。"
		s.renderWebPage(w, "login", data)
		return
	}
	s.loginLimiter.success(loginKey)
	session, err := s.service.CreateWebSession(r.Context(), account.OwnerID)
	if err != nil {
		data.Error = "登录会话创建失败，请重试。"
		s.renderWebPage(w, "login", data)
		return
	}
	s.setWebSessionCookie(w, r, session.Token, session.Record.ExpiresAt)
	http.Redirect(w, r, data.ReturnTo, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	_ = s.service.RevokeWebSession(r.Context(), session.ID, session.OwnerID)
	s.clearWebSessionCookie(w, r)
	s.redirectPublic(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleApp(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	s.renderAppPage(w, r, session, "overview")
}

func (s *Server) handleAppPage(page string) func(http.ResponseWriter, *http.Request, store.WebSessionRecord) {
	return func(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
		s.renderAppPage(w, r, session, page)
	}
}

func (s *Server) renderAppPage(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord, page string) {
	pageTitles := map[string]string{
		"overview":    "概览",
		"machines":    "设备管理",
		"oauth":       "OAuth 授权",
		"direct-keys": "临时直连密钥",
		"tokens":      "连接令牌",
		"security":    "账户安全",
		"mcp":         "MCP 服务",
		"system":      "运行状态",
	}
	pageTitle, ok := pageTitles[page]
	if !ok {
		http.NotFound(w, r)
		return
	}
	machines, err := s.service.ListMachines(r.Context(), session.OwnerID)
	if err != nil {
		http.Error(w, "Failed to load machines", http.StatusInternalServerError)
		return
	}
	clients, err := s.service.Store().ListOAuthClientsForOwner(r.Context(), session.OwnerID)
	if err != nil {
		http.Error(w, "Failed to load OAuth clients", http.StatusInternalServerError)
		return
	}
	authorizations, err := s.service.Store().ListOAuthAuthorizations(r.Context(), session.OwnerID)
	if err != nil {
		http.Error(w, "Failed to load OAuth authorizations", http.StatusInternalServerError)
		return
	}
	tokens, err := s.service.ListConnectionTokens(r.Context(), session.OwnerID)
	if err != nil {
		http.Error(w, "Failed to load access tokens", http.StatusInternalServerError)
		return
	}
	directKeys, err := s.service.ListDirectAccessKeys(r.Context(), session.OwnerID)
	if err != nil {
		http.Error(w, "Failed to load direct access keys", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	data := appPageData{
		Page:        page,
		PageTitle:   pageTitle,
		Version:     s.service.Version(),
		BasePath:    s.publicBasePath(r),
		BaseURL:     strings.TrimRight(s.publicBaseURL(r), "/"),
		Username:    session.Username,
		DisplayName: session.DisplayName,
		CSRFToken:   session.CSRFToken,
	}
	data.MCPURL = data.BaseURL + "/mcp"
	data.DirectURL = data.BaseURL + "/direct/v1"
	machineLabels := make(map[string]string, len(machines))
	for _, machine := range machines {
		machineLabels[machine.MachineID] = machine.DisplayName
	}
	for _, machine := range machines {
		view := machinePageView{MachineView: machine, LastSeen: "从未"}
		if machine.LastSeenAt != nil {
			view.LastSeen = formatWebTime(*machine.LastSeenAt)
		}
		if machine.Online {
			data.OnlineMachines++
		}
		data.Machines = append(data.Machines, view)
	}
	for _, client := range clients {
		data.Clients = append(data.Clients, oauthClientPageView{
			ClientID:     client.ClientID,
			ClientName:   client.ClientName,
			RedirectURIs: client.RedirectURIs,
			GrantTypes:   strings.Join(client.GrantTypes, ", "),
			Scope:        client.Scope,
			CreatedAt:    formatWebTime(client.CreatedAt),
		})
	}
	for _, token := range tokens {
		status := "有效"
		if token.RevokedAt != nil {
			status = "已撤销"
		} else if token.ExpiresAt != nil && !token.ExpiresAt.After(now) {
			status = "已过期"
		} else {
			data.ActiveTokens++
		}
		label := strings.TrimSpace(token.Label)
		if label == "" {
			label = "连接令牌"
		}
		lastUsed := "尚未使用"
		if token.LastUsedAt != nil {
			lastUsed = formatWebTime(*token.LastUsedAt)
		}
		expires := "长期有效"
		if token.ExpiresAt != nil {
			expires = formatWebTime(*token.ExpiresAt)
		}
		data.Tokens = append(data.Tokens, apiTokenPageView{
			ID:         token.ID,
			Label:      label,
			CreatedAt:  formatWebTime(token.CreatedAt),
			LastUsedAt: lastUsed,
			ExpiresAt:  expires,
			Status:     status,
		})
	}
	for _, directKey := range directKeys {
		status := "有效"
		if directKey.RevokedAt != nil {
			status = "已撤销"
		} else if !directKey.ExpiresAt.After(now) {
			status = "已过期"
		} else {
			data.ActiveDirectKeys++
		}
		lastUsed := "尚未使用"
		if directKey.LastUsedAt != nil {
			lastUsed = formatWebTime(*directKey.LastUsedAt)
		}
		machine := "全部设备"
		if directKey.MachineID != "" {
			machine = machineLabels[directKey.MachineID]
			if machine == "" {
				machine = directKey.MachineID
			}
		}
		data.DirectKeys = append(data.DirectKeys, directKeyPageView{
			ID: directKey.ID, Label: directKey.Label,
			Scopes: directScopeWebLabel(directKey.Scopes), Machine: machine, RateLimit: directKey.RateLimitPerMinute,
			CreatedAt: formatWebTime(directKey.CreatedAt), LastUsedAt: lastUsed,
			ExpiresAt: formatWebTime(directKey.ExpiresAt), Status: status,
		})
	}
	for _, authorization := range authorizations {
		status := "有效"
		if authorization.RevokedAt != nil {
			status = "已撤销"
		} else if !authorization.ExpiresAt.After(now) {
			status = "已过期"
		} else {
			data.ActiveAuthorizations++
		}
		lastUsed := "尚未使用"
		if authorization.LastUsedAt != nil {
			lastUsed = formatWebTime(*authorization.LastUsedAt)
		}
		data.Authorizations = append(data.Authorizations, oauthAuthorizationPageView{
			AuthorizationID: authorization.AuthorizationID,
			ClientID:        authorization.ClientID,
			ClientName:      authorization.ClientName,
			Scopes:          strings.Join(authorization.Scopes, " "),
			CreatedAt:       formatWebTime(authorization.CreatedAt),
			LastUsedAt:      lastUsed,
			Status:          status,
		})
	}
	switch r.URL.Query().Get("notice") {
	case "machine-note-updated":
		data.Notice = "管理员备注已保存。"
	case "machine-revoked":
		data.Notice = "设备已撤销。"
	case "machine-deleted":
		data.Notice = "设备已删除。"
	case "authorization-revoked":
		data.Notice = "OAuth 授权已撤销。"
	case "authorization-deleted":
		data.Notice = "OAuth 授权已删除。"
	case "client-deleted":
		data.Notice = "OAuth 客户端已删除。"
	case "token-revoked":
		data.Notice = "连接令牌已撤销。"
	case "token-deleted":
		data.Notice = "连接令牌已删除。"
	case "direct-key-revoked":
		data.Notice = "临时直连密钥已撤销。"
	case "direct-key-deleted":
		data.Notice = "临时直连密钥已删除。"
	case "password-changed":
		data.Notice = "密码已更新，其他网页登录会话已退出。"
	}
	switch r.URL.Query().Get("error") {
	case "password-mismatch":
		data.Error = "两次输入的新密码不一致。"
	case "password-invalid":
		data.Error = "当前密码不正确，或新密码不符合要求。"
	case "token-invalid":
		data.Error = "连接令牌有效期参数无效，请重新选择。"
	case "token-create":
		data.Error = "无法创建连接令牌。请检查名称和有效期，或先撤销不再使用的令牌。"
	case "direct-key-invalid":
		data.Error = "临时直连密钥参数无效。高权限密钥最长 24 小时，只读密钥最长 7 天。"
	case "direct-key-create":
		data.Error = "无法创建临时直连密钥。请检查名称、设备、权限、有效期和频率限制。"
	}
	s.renderWebPage(w, "app", data)
}

func (s *Server) handleAppMCPDiagnostics(w http.ResponseWriter, _ *http.Request, session store.WebSessionRecord) {
	writeJSON(w, http.StatusOK, s.mcpDiagnostics.snapshot(session.OwnerID))
}

func (s *Server) handleAppMachineNote(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.UpdateMachineAdminNote(r.Context(), session.OwnerID, r.PathValue("machineId"), r.PostForm.Get("admin_note"), remoteIP(r)); err != nil {
		http.Error(w, "Unable to update machine note", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/machines?notice=machine-note-updated", http.StatusSeeOther)
}

func (s *Server) handleAppMachineRevoke(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.RevokeMachine(r.Context(), session.OwnerID, r.PathValue("machineId"), remoteIP(r)); err != nil {
		http.Error(w, "Unable to revoke machine", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/machines?notice=machine-revoked", http.StatusSeeOther)
}

func (s *Server) handleAppMachineDelete(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.DeleteMachine(r.Context(), session.OwnerID, r.PathValue("machineId"), remoteIP(r)); err != nil {
		http.Error(w, "Unable to delete machine", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/machines?notice=machine-deleted", http.StatusSeeOther)
}

func (s *Server) handleAppAuthorizationRevoke(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.Store().RevokeOAuthAuthorization(
		r.Context(), session.OwnerID, r.PathValue("authorizationId"), time.Now().UTC(),
	); err != nil {
		http.Error(w, "Unable to revoke OAuth authorization", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/access/oauth?notice=authorization-revoked", http.StatusSeeOther)
}

func (s *Server) handleAppAuthorizationDelete(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.Store().DeleteOAuthAuthorization(
		r.Context(), session.OwnerID, r.PathValue("authorizationId"), time.Now().UTC(),
	); err != nil {
		http.Error(w, "Unable to delete OAuth authorization", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/access/oauth?notice=authorization-deleted", http.StatusSeeOther)
}

func (s *Server) handleAppClientDelete(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.Store().DeleteOAuthClientForOwner(r.Context(), session.OwnerID, r.PathValue("clientId")); err != nil {
		http.Error(w, "Unable to delete OAuth client", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/access/oauth?notice=client-deleted", http.StatusSeeOther)
}

func (s *Server) handleAppPasswordChange(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	current := r.PostForm.Get("current_password")
	password := r.PostForm.Get("new_password")
	if password == "" || password != r.PostForm.Get("password_confirm") {
		s.redirectPublic(w, r, "/app/access/security?error=password-mismatch", http.StatusSeeOther)
		return
	}
	if err := s.service.ChangeOwnerPassword(
		r.Context(), session.OwnerID, current, password, session.ID, remoteIP(r),
	); err != nil {
		s.redirectPublic(w, r, "/app/access/security?error=password-invalid", http.StatusSeeOther)
		return
	}
	s.redirectPublic(w, r, "/app/access/security?notice=password-changed", http.StatusSeeOther)
}

func (s *Server) handleAppTokenCreate(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("expires_days")))
	if err != nil || (days != 0 && days != 30 && days != 90 && days != 365) {
		s.redirectPublic(w, r, "/app/access/tokens?error=token-invalid", http.StatusSeeOther)
		return
	}
	result, err := s.service.CreateConnectionToken(
		r.Context(),
		session.OwnerID,
		r.PostForm.Get("label"),
		time.Duration(days)*24*time.Hour,
		remoteIP(r),
	)
	if err != nil {
		s.redirectPublic(w, r, "/app/access/tokens?error=token-create", http.StatusSeeOther)
		return
	}
	expires := "长期有效"
	if result.Record.ExpiresAt != nil {
		expires = formatWebTime(*result.Record.ExpiresAt)
	}
	s.renderWebPage(w, "token", tokenPageData{
		BasePath: s.publicBasePath(r),
		Label:    result.Record.Label,
		Token:    result.Token,
		Expires:  expires,
	})
}

func (s *Server) handleAppDirectKeyCreate(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("expires_minutes")))
	if err != nil || (minutes != 10 && minutes != 60 && minutes != 360 && minutes != 1440 && minutes != 10080) {
		s.redirectPublic(w, r, "/app/access/direct-keys?error=direct-key-invalid", http.StatusSeeOther)
		return
	}
	rateLimit, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("rate_limit")))
	if err != nil || rateLimit < 1 || rateLimit > 600 {
		s.redirectPublic(w, r, "/app/access/direct-keys?error=direct-key-invalid", http.StatusSeeOther)
		return
	}
	result, err := s.service.CreateDirectAccessKey(
		r.Context(), session.OwnerID, r.PostForm.Get("label"), r.PostForm.Get("machine_id"),
		r.PostForm["scope"], time.Duration(minutes)*time.Minute, rateLimit, remoteIP(r),
	)
	if err != nil {
		s.redirectPublic(w, r, "/app/access/direct-keys?error=direct-key-create", http.StatusSeeOther)
		return
	}
	machine := "全部设备"
	if result.Record.MachineID != "" {
		machine = result.Record.MachineID
		if item, getErr := s.service.GetMachine(r.Context(), session.OwnerID, result.Record.MachineID); getErr == nil {
			machine = item.DisplayName
		}
	}
	s.renderWebPage(w, "direct-key", directKeyPageData{
		BasePath: s.publicBasePath(r), DirectURL: strings.TrimRight(s.publicBaseURL(r), "/") + "/direct/v1",
		Label: result.Record.Label, Token: result.Token, Expires: formatWebTime(result.Record.ExpiresAt),
		Scopes: directScopeWebLabel(result.Record.Scopes), Machine: machine, RateLimit: result.Record.RateLimitPerMinute,
	})
}

func (s *Server) handleAppDirectKeyRevoke(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.RevokeDirectAccessKey(r.Context(), session.OwnerID, r.PathValue("keyId"), remoteIP(r)); err != nil {
		http.Error(w, "Unable to revoke direct access key", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/access/direct-keys?notice=direct-key-revoked", http.StatusSeeOther)
}

func (s *Server) handleAppDirectKeyDelete(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.DeleteDirectAccessKey(r.Context(), session.OwnerID, r.PathValue("keyId"), remoteIP(r)); err != nil {
		http.Error(w, "Unable to delete direct access key", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/access/direct-keys?notice=direct-key-deleted", http.StatusSeeOther)
}

func directScopeWebLabel(scopes []string) string {
	if len(scopes) == 0 {
		return "只读"
	}
	labels := map[string]string{
		core.DirectScopeFilesWrite: "文件写入", core.DirectScopeShell: "Shell / Build", core.DirectScopeJobs: "任务取消",
		core.DirectScopeGit: "Git 写入", core.DirectScopeBrowser: "浏览器 / 截图", core.DirectScopeAI: "AI 控制",
		core.DirectScopeContextWrite: "上下文写入", core.DirectScopeArtifactWrite: "文件上传 / 发布",
	}
	items := []string{"只读"}
	for _, scope := range scopes {
		label := labels[scope]
		if label == "" {
			label = scope
		}
		items = append(items, label)
	}
	return strings.Join(items, " + ")
}

func (s *Server) handleAppTokenRevoke(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.RevokeConnectionToken(
		r.Context(), session.OwnerID, r.PathValue("tokenId"), remoteIP(r),
	); err != nil {
		http.Error(w, "Unable to revoke access token", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/access/tokens?notice=token-revoked", http.StatusSeeOther)
}

func (s *Server) handleAppTokenDelete(w http.ResponseWriter, r *http.Request, session store.WebSessionRecord) {
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if err := s.service.DeleteConnectionToken(
		r.Context(), session.OwnerID, r.PathValue("tokenId"), remoteIP(r),
	); err != nil {
		http.Error(w, "Unable to delete access token", http.StatusBadRequest)
		return
	}
	s.redirectPublic(w, r, "/app/access/tokens?notice=token-deleted", http.StatusSeeOther)
}

func (s *Server) handleWebAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.PathValue("file"))
	if name != "app.css" && name != "admin.css" && name != "setup.js" && name != "app.js" {
		http.NotFound(w, r)
		return
	}
	raw, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	// Assets are embedded but not content-hashed. Revalidate on each page load so
	// a Hub upgrade never mixes new HTML with an hour-old CSS/JS response.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

func (s *Server) webSessionOnly(next func(http.ResponseWriter, *http.Request, store.WebSessionRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := s.currentWebSession(r)
		if err != nil {
			returnTo := s.publicURL(r, r.URL.RequestURI())
			loginURL := s.publicURL(r, "/login") + "?return_to=" + url.QueryEscape(returnTo)
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		next(w, r, session)
	}
}

func (s *Server) webSessionJSONOnly(next func(http.ResponseWriter, *http.Request, store.WebSessionRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := s.currentWebSession(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: protocolv1.ProtocolError{Code: "UNAUTHORIZED", Message: "authentication required", Retryable: false}})
			return
		}
		next(w, r, session)
	}
}

func (s *Server) currentWebSession(r *http.Request) (store.WebSessionRecord, error) {
	cookie, err := r.Cookie(webSessionCookieName)
	if err != nil || cookie.Value == "" {
		return store.WebSessionRecord{}, store.ErrUnauthorized
	}
	return s.service.AuthenticateWebSession(r.Context(), cookie.Value)
}

func (s *Server) setWebSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	base, _ := s.oauthBaseURL(r)
	cookiePath := "/"
	secure := r.TLS != nil
	if base != nil {
		if strings.TrimSpace(base.Path) != "" {
			cookiePath = strings.TrimRight(base.Path, "/")
		}
		secure = base.Scheme == "https"
	}
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookieName, Value: token, Path: cookiePath,
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearWebSessionCookie(w http.ResponseWriter, r *http.Request) {
	base, _ := s.oauthBaseURL(r)
	cookiePath := "/"
	secure := r.TLS != nil
	if base != nil {
		if strings.TrimSpace(base.Path) != "" {
			cookiePath = strings.TrimRight(base.Path, "/")
		}
		secure = base.Scheme == "https"
	}
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookieName, Path: cookiePath, MaxAge: -1,
		Expires: time.Unix(1, 0), HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) verifyCSRF(w http.ResponseWriter, r *http.Request, expected string) bool {
	if !s.parseWebForm(w, r) {
		return false
	}
	actual := r.PostForm.Get("csrf_token")
	if len(actual) != len(expected) || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		http.Error(w, "Invalid request", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) parseWebForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxControlMessageBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) renderWebPage(w http.ResponseWriter, name string, data any) {
	s.renderWebPageStatus(w, name, data, http.StatusOK)
}

func (s *Server) renderWebPageStatus(w http.ResponseWriter, name string, data any, status int) {
	tmpl := webTemplates[name]
	if tmpl == nil {
		http.Error(w, "Template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := tmpl.Execute(w, data); err != nil {
		s.config.Logger.Error("render web page", "template", name, "error", err)
	}
}

func (s *Server) renderOAuthAuthorizePage(
	w http.ResponseWriter,
	r *http.Request,
	session store.WebSessionRecord,
	client store.OAuthClientRecord,
	values url.Values,
	errorMessage string,
) {
	fieldNames := []string{
		"response_type", "client_id", "redirect_uri", "code_challenge",
		"code_challenge_method", "scope", "state", "resource",
	}
	fields := make([]hiddenField, 0, len(fieldNames))
	for _, name := range fieldNames {
		fields = append(fields, hiddenField{Name: name, Value: values.Get(name)})
	}
	scopeLabel := "MCP 访问"
	description := "客户端获得授权后，可以通过 Fast Spider MCP 使用当前账户下的设备及其本机能力。"
	if callbackOrigin := oauthAuthorizeCallbackOrigin(values.Get("redirect_uri")); callbackOrigin != "" {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self' "+callbackOrigin+"; script-src 'self'; style-src 'self'")
	}
	s.renderWebPage(w, "authorize", authorizePageData{
		BasePath:    s.publicBasePath(r),
		DisplayName: session.DisplayName,
		ClientName:  client.ClientName,
		ClientID:    client.ClientID,
		ScopeLabel:  scopeLabel,
		Description: description,
		CSRFToken:   session.CSRFToken,
		Error:       errorMessage,
		Fields:      fields,
	})
}

func oauthAuthorizeCallbackOrigin(raw string) string {
	callback, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || callback.Host == "" || (callback.Scheme != "http" && callback.Scheme != "https") {
		return ""
	}
	return callback.Scheme + "://" + callback.Host
}

func (s *Server) publicBaseURL(r *http.Request) string {
	base, err := s.oauthBaseURL(r)
	if err != nil {
		return ""
	}
	return strings.TrimRight(base.String(), "/")
}

func (s *Server) publicBasePath(r *http.Request) string {
	base, err := s.oauthBaseURL(r)
	if err != nil {
		return ""
	}
	return strings.TrimRight(base.Path, "/")
}

func (s *Server) publicURL(r *http.Request, suffix string) string {
	base, err := s.oauthBaseURL(r)
	if err != nil {
		return suffix
	}
	return oauthURL(base, suffix)
}

func (s *Server) redirectPublic(w http.ResponseWriter, r *http.Request, suffix string, status int) {
	http.Redirect(w, r, s.publicURL(r, suffix), status)
}

func (s *Server) safeReturnTo(r *http.Request, raw string) string {
	fallback := s.publicURL(r, "/app")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	candidate, err := url.Parse(raw)
	if err != nil {
		return fallback
	}
	base, err := s.oauthBaseURL(r)
	if err != nil {
		return fallback
	}
	if !candidate.IsAbs() {
		if !strings.HasPrefix(candidate.Path, "/") {
			return fallback
		}
		candidate.Scheme = base.Scheme
		candidate.Host = base.Host
	}
	if !strings.EqualFold(candidate.Scheme, base.Scheme) || !strings.EqualFold(candidate.Host, base.Host) {
		return fallback
	}
	basePath := strings.TrimRight(base.Path, "/")
	if basePath != "" && candidate.Path != basePath && !strings.HasPrefix(candidate.Path, basePath+"/") {
		return fallback
	}
	candidate.Fragment = ""
	return candidate.String()
}

func formatWebTime(value time.Time) string {
	return value.Local().Format("2006-01-02 15:04")
}
