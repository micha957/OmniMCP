package core

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// approvalGuard protects the human decision surface. MCP's API token must not
// open it, otherwise any model holding that token could approve its own writes.
// With OMNIMCP_APPROVAL_TOKEN set that token is required (and remote review is
// possible); without it only loopback requests are accepted. Writes also refuse
// cross-site browser requests, so a web page cannot approve through localhost.
func approvalGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token := strings.TrimSpace(os.Getenv("OMNIMCP_APPROVAL_TOKEN")); token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				approvalFail(w, http.StatusUnauthorized, fmt.Errorf("需要确认口令"))
				return
			}
		} else if !loopbackRequest(r) {
			approvalFail(w, http.StatusForbidden, fmt.Errorf("未设置 OMNIMCP_APPROVAL_TOKEN 时只能在本机确认"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				approvalFail(w, http.StatusForbidden, fmt.Errorf("确认操作须以 JSON 提交，且不能来自其他网站"))
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					approvalFail(w, http.StatusForbidden, fmt.Errorf("确认操作不能来自其他网站"))
					return
				}
			}
		}
		next(w, r)
	}
}

// loopbackRequest checks both the peer and the Host header; the latter stops DNS
// rebinding from reaching the queue through a browser on this machine.
func loopbackRequest(r *http.Request) bool {
	isLoopback := func(host string) bool {
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		ip := net.ParseIP(host)
		return host == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return isLoopback(r.RemoteAddr) && isLoopback(r.Host)
}

func approvalFail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func approvalBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	b, err := io.ReadAll(r.Body)
	if err == nil && len(b) > 0 {
		err = json.Unmarshal(b, target)
	}
	if err != nil {
		approvalFail(w, http.StatusBadRequest, fmt.Errorf("请求内容无效或过大"))
		return false
	}
	return true
}

func (d *Dashboard) registerApprovals(mux *http.ServeMux) {
	mux.HandleFunc("GET /approvals", func(w http.ResponseWriter, r *http.Request) {
		b, _ := dashboardFS.ReadFile("web/approvals.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		_, _ = w.Write(b)
	})
	store := d.Server.Approvals
	mux.HandleFunc("GET /api/approvals", approvalGuard(func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"items": store.List(r.URL.Query().Get("status"), 200)})
	}))
	mux.HandleFunc("GET /api/approvals/{id}", approvalGuard(func(w http.ResponseWriter, r *http.Request) {
		a, ok := store.Get(r.PathValue("id"))
		if !ok {
			approvalFail(w, http.StatusNotFound, fmt.Errorf("审批项不存在"))
			return
		}
		jsonOut(w, a)
	}))
	decide := func(run func(id, hash string) (Approval, error)) http.HandlerFunc {
		return approvalGuard(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Hash string `json:"hash"`
			}
			if !approvalBody(w, r, &in) {
				return
			}
			a, err := run(r.PathValue("id"), in.Hash)
			if err != nil {
				approvalFail(w, http.StatusConflict, err)
				return
			}
			jsonOut(w, a)
		})
	}
	mux.HandleFunc("POST /api/approvals/{id}/approve", decide(d.Server.Approve))
	mux.HandleFunc("POST /api/approvals/{id}/retry", decide(d.Server.Retry))
	mux.HandleFunc("POST /api/approvals/{id}/reject", approvalGuard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reason string `json:"reason"`
		}
		if !approvalBody(w, r, &in) {
			return
		}
		a, err := store.Reject(r.PathValue("id"), in.Reason)
		if err != nil {
			approvalFail(w, http.StatusConflict, err)
			return
		}
		jsonOut(w, a)
	}))
	mux.HandleFunc("PATCH /api/approvals/{id}", approvalGuard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Args json.RawMessage `json:"args"`
		}
		if !approvalBody(w, r, &in) {
			return
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(in.Args, &m) != nil || m == nil {
			approvalFail(w, http.StatusBadRequest, fmt.Errorf("参数必须是 JSON 对象"))
			return
		}
		delete(m, "_account_id")
		delete(m, "_note")
		args, _ := json.Marshal(m)
		existing, ok := store.Get(r.PathValue("id"))
		if !ok {
			approvalFail(w, http.StatusNotFound, fmt.Errorf("审批项不存在"))
			return
		}
		if ref, found := d.Server.Registry.Lookup(existing.Tool); found {
			if err := requiredArgsMissing(ref.t.InputSchema, args); err != nil {
				approvalFail(w, http.StatusBadRequest, err)
				return
			}
		}
		a, err := store.Edit(r.PathValue("id"), args)
		if err != nil {
			approvalFail(w, http.StatusConflict, err)
			return
		}
		jsonOut(w, a)
	}))
}
