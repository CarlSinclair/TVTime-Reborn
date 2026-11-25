package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CarlSinclair/csuit-libs/auditlog"
	"tvt/auth"
	"tvt/logger"
)

var auditEntityKeys = []string{"comment", "target", "server", "key", "host", "slug", "id"}

func (a *App) initAudit() error {
	l, err := auditlog.New(auditlog.Config{DB: a.Store.DB, TimeUnix: true, Silent: true,
		UsersTable: "users", UsersIDCol: "id", UsersLabelExpr: "u.username"})
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	a.auditLog = l
	return nil
}

func (a *App) audit(r *http.Request, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	logger.Info("%s", line)
	if a.auditLog == nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return
	}
	var uid int64
	details := map[string]string{}
	for _, f := range fields[2:] {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" || k == "ip" {
			continue
		}
		if k == "uid" {
			uid, _ = strconv.ParseInt(v, 10, 64)
			continue
		}
		details[k] = strings.Trim(v, `"`)
	}
	var entity string
	for _, k := range auditEntityKeys {
		if v, ok := details[k]; ok {
			entity = v
			break
		}
	}
	kv := []string{"entity", entity}
	for k, v := range details {
		kv = append(kv, k, v)
	}
	a.auditLog.Record(r, uid, fields[1], kv...)
}

func (a *App) auditOptions(r *http.Request, lang string) auditlog.PageOptions {
	t := func(key string) string { return a.I18n.T(lang, key) }
	return auditlog.PageOptions{
		Base: "/admin/audit",
		T:    t,
		Icon: func(name string) string { return "/static/icons.svg?v=" + assetVersion + "#fa-" + name },
		UserLabel: func(id int64) string {
			if u, err := a.Store.UserByID(id); err == nil && u != nil {
				return u.Username
			}
			return ""
		},
		UserLink: func(int64) string { return "/admin?tab=users" },
		EntityLink: func(e auditlog.Entry) string {
			switch e.ResourceType {
			case "user", "target":
				return "/admin?tab=users"
			case "server":
				return "/admin?tab=servers"
			case "comment":
				return "/admin?tab=reports"
			}
			return ""
		},
		Location: time.UTC,
	}
}

func (a *App) auditPage(w http.ResponseWriter, r *http.Request) {
	if _, err := a.needAdmin(r); err != nil {
		a.errorPage(w, r, http.StatusForbidden, "err.forbidden")
		return
	}
	o := a.auditOptions(r, a.lang(w, r, auth.UserFrom(r.Context())))
	if r.URL.Query().Get("format") == "csv" {
		a.auditLog.ServeCSV(w, r, o)
		return
	}
	v := a.auditLog.Page(r, o)
	if v.Err {
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	reports, servers, err := a.adminBadges()
	if err != nil {
		logger.Error("PAGE admin audit: badges: %v", err)
		a.errorPage(w, r, http.StatusInternalServerError, "err.internal")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	a.page(w, r, "admin", "nav.admin", map[string]any{"Tab": "audit", "Body": auditlog.Render(v, o), "ReportsBadge": reports, "ServersBadge": servers})
}
