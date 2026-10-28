package siteauth

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

//go:embed ui/*
var assets embed.FS

func (a *Auth) session(r *http.Request) (string, int64, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || len(c.Value) != 43 {
		return "", 0, false
	}
	hash := digest(c.Value)
	var at int64
	err = a.db.QueryRow("SELECT auth_at FROM sessions WHERE token_hash=? AND expires_at>?", hash, a.now().Unix()).Scan(&at)
	return hash, at, err == nil
}
func (a *Auth) Authenticated(r *http.Request) bool { _, _, ok := a.session(r); return ok }
func (a *Auth) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !a.Authenticated(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}

func (a *Auth) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		raw, _ := assets.ReadFile("ui/login.html")
		_, _ = w.Write(raw)
	})
	for _, name := range []string{"login.js", "login.css"} {
		mux.HandleFunc("GET /auth/"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			if name == "login.js" {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			} else {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			}
			raw, _ := assets.ReadFile("ui/" + name)
			_, _ = w.Write(raw)
		})
	}
	mux.HandleFunc("GET /auth/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		authenticated := a.Authenticated(r)
		n := 0
		if authenticated {
			if err := a.db.QueryRow("SELECT count(*) FROM credentials").Scan(&n); err != nil {
				failure(w, 500, "无法读取账户")
				return
			}
		}
		reply(w, 200, map[string]any{"authenticated": authenticated, "passkeys": n})
	})
	mux.HandleFunc("POST /auth/code", a.write(a.code, true))
	mux.HandleFunc("POST /auth/logout", a.write(a.logout, false))
	mux.HandleFunc("POST /auth/passkey/register/begin", a.write(a.registerBegin, false))
	mux.HandleFunc("POST /auth/passkey/register/finish", a.write(a.registerFinish, false))
	mux.HandleFunc("POST /auth/passkey/login/begin", a.write(a.loginBegin, true))
	mux.HandleFunc("POST /auth/passkey/login/finish", a.write(a.loginFinish, true))
}

func (a *Auth) write(next http.HandlerFunc, limited bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != a.origin {
			failure(w, 403, "请求来源不匹配")
			return
		}
		if limited {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			if !a.allowAttempt(ip) {
				failure(w, 429, "尝试过于频繁,请一分钟后重试")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		next(w, r)
	}
}
func (a *Auth) code(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Code string `json:"code"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		failure(w, 400, "登录码格式无效")
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		failure(w, 400, "登录码格式无效")
		return
	}
	token, err := a.consumeCode(payload.Code)
	if err != nil {
		failure(w, 401, "登录码无效、已使用或已过期")
		return
	}
	// Rotate an existing browser session when authenticating again.
	if hash, _, ok := a.session(r); ok {
		_, _ = a.db.Exec("DELETE FROM sessions WHERE token_hash=?", hash)
	}
	cookie(w, sessionCookie, token, int(sessionLifetime.Seconds()))
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	hash, _, ok := a.session(r)
	if !ok {
		failure(w, 401, "请先登录")
		return
	}
	if _, err := a.db.Exec("DELETE FROM sessions WHERE token_hash=?", hash); err != nil {
		failure(w, 500, "退出失败")
		return
	}
	cookie(w, sessionCookie, "", -1)
	cookie(w, challengeCookie, "", -1)
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *Auth) recentSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	hash, at, ok := a.session(r)
	if !ok {
		failure(w, 401, "请先登录")
		return "", false
	}
	if a.now().Sub(time.Unix(at, 0)) > 10*time.Minute {
		failure(w, 401, "请重新用 Passkey 或登录码验证后再添加 Passkey")
		return "", false
	}
	return hash, true
}
func (a *Auth) challenge(w http.ResponseWriter, r *http.Request, kind, binding string) (webauthn.SessionData, bool) {
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		failure(w, 400, "验证已过期,请重新开始")
		return webauthn.SessionData{}, false
	}
	data, err := a.takeChallenge(c.Value, kind, binding)
	cookie(w, challengeCookie, "", -1)
	if err != nil {
		failure(w, 400, "验证已过期或已使用,请重新开始")
		return data, false
	}
	return data, true
}
func (a *Auth) registerBegin(w http.ResponseWriter, r *http.Request) {
	hash, ok := a.recentSession(w, r)
	if !ok {
		return
	}
	u, err := a.loadUser()
	if err != nil {
		failure(w, 500, "无法读取账户")
		return
	}
	if len(u.credentials) >= 10 {
		failure(w, 400, "最多添加 10 个 Passkey")
		return
	}
	options, data, err := a.wa.BeginRegistration(u, webauthn.WithExclusions(webauthn.Credentials(u.credentials).CredentialDescriptors()))
	if err != nil {
		failure(w, 500, "无法开始注册")
		return
	}
	token, err := a.storeChallenge("register", hash, data)
	if err != nil {
		failure(w, 500, "无法保存验证")
		return
	}
	cookie(w, challengeCookie, token, int(challengeLifetime.Seconds()))
	reply(w, 200, options)
}
func (a *Auth) registerFinish(w http.ResponseWriter, r *http.Request) {
	hash, ok := a.recentSession(w, r)
	if !ok {
		return
	}
	data, ok := a.challenge(w, r, "register", hash)
	if !ok {
		return
	}
	a.ceremonyMu.Lock()
	defer a.ceremonyMu.Unlock()
	u, err := a.loadUser()
	if err != nil {
		failure(w, 500, "无法读取账户")
		return
	}
	if len(u.credentials) >= 10 {
		failure(w, 400, "最多添加 10 个 Passkey")
		return
	}
	credential, err := a.wa.FinishRegistration(u, data, r)
	if err != nil {
		failure(w, 400, "Passkey 验证失败,请重新注册")
		return
	}
	if err = a.saveCredential(credential, true); err != nil {
		failure(w, 500, "无法保存 Passkey")
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (a *Auth) loginBegin(w http.ResponseWriter, r *http.Request) {
	options, data, err := a.wa.BeginDiscoverableLogin()
	if err != nil {
		failure(w, 500, "无法开始登录")
		return
	}
	token, err := a.storeChallenge("login", "", data)
	if err != nil {
		failure(w, 500, "无法保存验证")
		return
	}
	cookie(w, challengeCookie, token, int(challengeLifetime.Seconds()))
	reply(w, 200, options)
}
func (a *Auth) loginFinish(w http.ResponseWriter, r *http.Request) {
	data, ok := a.challenge(w, r, "login", "")
	if !ok {
		return
	}
	a.ceremonyMu.Lock()
	defer a.ceremonyMu.Unlock()
	u, err := a.loadUser()
	if err != nil {
		failure(w, 500, "无法读取账户")
		return
	}
	credential, err := a.wa.FinishDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		if !bytes.Equal(userHandle, u.id) {
			return nil, errDenied
		}
		for _, c := range u.credentials {
			if bytes.Equal(rawID, c.ID) {
				return u, nil
			}
		}
		return nil, errDenied
	}, data, r)
	if err != nil || credential == nil || credential.Authenticator.CloneWarning {
		failure(w, 401, "Passkey 登录失败,请重试")
		return
	}
	if err = a.saveCredential(credential, false); err != nil {
		failure(w, 500, "无法保存验证结果")
		return
	}
	token, err := a.newSession()
	if err != nil {
		failure(w, 500, "无法创建会话")
		return
	}
	if hash, _, ok := a.session(r); ok {
		_, _ = a.db.Exec("DELETE FROM sessions WHERE token_hash=?", hash)
	}
	cookie(w, sessionCookie, token, int(sessionLifetime.Seconds()))
	reply(w, 200, map[string]bool{"ok": true})
}
