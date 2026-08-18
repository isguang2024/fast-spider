package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

const adminSessionCookieName = "fast_spider_admin_session"

type adminLoginPageData struct {
	BasePath string
	Error    string
	Username string
}

type adminPageData struct {
	BasePath  string
	Username  string
	CSRFToken string
	Error     string
	Notice    string
	Users     []core.OwnerAccountView
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if _, err := s.currentAdminSession(r); err == nil {
		s.redirectPublic(w, r, "/admin", http.StatusFound)
		return
	}
	data := adminLoginPageData{BasePath: s.publicBasePath(r)}
	if r.Method == http.MethodGet {
		s.renderWebPage(w, "admin-login", data)
		return
	}
	loginKey := "admin:" + remoteIP(r)
	now := time.Now().UTC()
	if s.loginLimiter.blocked(loginKey, now) {
		w.Header().Set("Retry-After", "900")
		data.Error = "登录失败次数过多，请 15 分钟后再试。"
		s.renderWebPageStatus(w, "admin-login", data, http.StatusTooManyRequests)
		return
	}
	if !s.parseWebForm(w, r) {
		return
	}
	data.Username = strings.TrimSpace(r.PostForm.Get("username"))
	account, err := s.service.LoginAdmin(r.Context(), data.Username, r.PostForm.Get("password"), remoteIP(r))
	if err != nil {
		s.loginLimiter.failure(loginKey, now)
		data.Error = "管理员用户名或密码不正确。"
		s.renderWebPage(w, "admin-login", data)
		return
	}
	s.loginLimiter.success(loginKey)
	session, err := s.service.CreateAdminSession(r.Context(), account.ID)
	if err != nil {
		data.Error = "管理员登录会话创建失败，请重试。"
		s.renderWebPage(w, "admin-login", data)
		return
	}
	s.setAdminSessionCookie(w, r, session.Token, session.Record.ExpiresAt)
	s.redirectPublic(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, session store.AdminSessionRecord) {
	data := adminPageData{BasePath: s.publicBasePath(r), Username: session.Username, CSRFToken: session.CSRFToken}
	users, err := s.service.ListUsers(r.Context())
	if err != nil {
		data.Error = "用户列表读取失败，请稍后重试。"
	} else {
		data.Users = users
	}
	switch r.URL.Query().Get("notice") {
	case "user-created":
		data.Notice = "用户创建成功。"
	}
	switch r.URL.Query().Get("error") {
	case "invalid":
		data.Error = "用户名、显示名称或密码格式不正确。"
	case "duplicate":
		data.Error = "用户名已存在。"
	case "create":
		data.Error = "用户创建失败，请检查输入后重试。"
	}
	s.renderWebPage(w, "admin", data)
}

func (s *Server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request, session store.AdminSessionRecord) {
	if !s.verifyAdminCSRF(w, r, session.CSRFToken) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	displayName := strings.TrimSpace(r.PostForm.Get("display_name"))
	password := r.PostForm.Get("password")
	if password == "" || password != r.PostForm.Get("password_confirm") {
		s.redirectPublic(w, r, "/admin?error=invalid", http.StatusSeeOther)
		return
	}
	if _, err := s.service.CreateUser(r.Context(), session.AdminID, username, displayName, password, remoteIP(r)); err != nil {
		if err == store.ErrConflict {
			s.redirectPublic(w, r, "/admin?error=duplicate", http.StatusSeeOther)
		} else {
			s.redirectPublic(w, r, "/admin?error=invalid", http.StatusSeeOther)
		}
		return
	}
	s.redirectPublic(w, r, "/admin?notice=user-created", http.StatusSeeOther)
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request, session store.AdminSessionRecord) {
	if !s.verifyAdminCSRF(w, r, session.CSRFToken) {
		return
	}
	_ = s.service.RevokeAdminSession(r.Context(), session.ID)
	s.clearAdminSessionCookie(w, r)
	s.redirectPublic(w, r, "/admin/login", http.StatusSeeOther)
}

func (s *Server) adminSessionOnly(next func(http.ResponseWriter, *http.Request, store.AdminSessionRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := s.currentAdminSession(r)
		if err != nil {
			http.Redirect(w, r, s.publicURL(r, "/admin/login"), http.StatusSeeOther)
			return
		}
		next(w, r, session)
	}
}

func (s *Server) currentAdminSession(r *http.Request) (store.AdminSessionRecord, error) {
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil || cookie.Value == "" {
		return store.AdminSessionRecord{}, store.ErrUnauthorized
	}
	return s.service.AuthenticateAdminSession(r.Context(), cookie.Value)
}

func (s *Server) adminCookiePath(r *http.Request) string {
	base := s.publicBasePath(r)
	if base == "" {
		return "/admin"
	}
	return base + "/admin"
}

func (s *Server) setAdminSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	secure := r.TLS != nil
	if base, err := s.oauthBaseURL(r); err == nil {
		secure = base.Scheme == "https"
	}
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookieName, Value: token, Path: s.adminCookiePath(r), Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearAdminSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil
	if base, err := s.oauthBaseURL(r); err == nil {
		secure = base.Scheme == "https"
	}
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookieName, Path: s.adminCookiePath(r), MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func (s *Server) verifyAdminCSRF(w http.ResponseWriter, r *http.Request, expected string) bool {
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
