package api

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coroot/coroot/api/forms"
	"github.com/coroot/coroot/collector"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/rbac"
	"github.com/coroot/coroot/utils"
	"github.com/go-ldap/ldap/v3"
	"golang.org/x/oauth2"
	"k8s.io/klog"
)

type Session struct {
	Id int `json:"id"`
}

const (
	AuthSecretSettingName = "auth_secret"
	SSOSettingName        = "sso"
	HashFunc              = crypto.SHA256
	SessionCookieName     = "coroot_session"
	SessionCookieTTL      = 7 * 24 * time.Hour
	SSOStateCookieName    = "coroot_sso_state"
	SSOStateCookieTTL     = 10 * time.Minute
)

type SSOSettings struct {
	Enabled     bool          `json:"enabled"`
	Provider    string        `json:"provider"`
	DefaultRole rbac.RoleName `json:"default_role"`
	ForceSSO    bool          `json:"force_sso"`
	SAML        SAMLSettings  `json:"saml,omitempty"`
	OIDC        OIDCSettings  `json:"oidc,omitempty"`
	LDAP        LDAPSettings  `json:"ldap,omitempty"`
}

type SAMLSettings struct {
	Metadata string `json:"metadata,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type OIDCSettings struct {
	IssuerURL    string `json:"issuer_url,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

type LDAPSettings struct {
	URL                string `json:"url,omitempty"`
	StartTLS           bool   `json:"start_tls,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	BindDN             string `json:"bind_dn,omitempty"`
	BindPassword       string `json:"bind_password,omitempty"`
	BaseDN             string `json:"base_dn,omitempty"`
	UserFilter         string `json:"user_filter,omitempty"`
	EmailAttribute     string `json:"email_attribute,omitempty"`
	NameAttribute      string `json:"name_attribute,omitempty"`
}

type ssoState struct {
	Nonce string `json:"nonce"`
	Next  string `json:"next"`
}

func (api *Api) AuthInit(anonymousRole string, adminPassword string) error {
	if anonymousRole != "" {
		role := rbac.RoleName(anonymousRole)
		roles, err := api.roles.GetRoles()
		if err != nil {
			return err
		}
		if !role.Valid(roles) {
			var names []rbac.RoleName
			for _, r := range roles {
				names = append(names, r.Name)
			}
			return fmt.Errorf("anonymous role must one of %s, got '%s'", names, role)
		}
		api.authAnonymousRole = role
		klog.Infoln("anonymous access enabled with the role:", role)
	}

	var secret string
	err := api.db.GetSetting(AuthSecretSettingName, &secret)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			secret = utils.RandomString(HashFunc.Size())
			err = api.db.SetSetting(AuthSecretSettingName, secret)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	api.authSecret = secret

	err = api.db.CreateAdminIfNotExists(adminPassword)
	if err != nil {
		return err
	}

	return nil
}

func (api *Api) Auth(h func(http.ResponseWriter, *http.Request, *db.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user := api.GetUser(r); user != nil {
			h(w, r, user)
			return
		}

		admin, err := api.db.DefaultAdminUserIsTheOnlyUser()
		if err != nil {
			klog.Errorln(err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		if admin != nil {
			http.Error(w, "set_admin_password", http.StatusUnauthorized)
		} else {
			http.Error(w, "", http.StatusUnauthorized)
		}
		return
	}
}

func (api *Api) getProjectByApiKey(apiKey string) (*db.Project, error) {
	projects, err := api.db.GetProjects()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if p.Multicluster() {
			continue
		}
		for _, key := range p.Settings.ApiKeys {
			if !key.IsEmpty() && key.Key == apiKey {
				return p, nil
			}
		}
	}
	return nil, nil
}

func (api *Api) ApiKeyAuth(h func(http.ResponseWriter, *http.Request, *db.Project)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get(collector.ApiKeyHeader)
		if apiKey == "" {
			klog.Warningln("no api key")
			http.Error(w, "no api key", http.StatusBadRequest)
			return
		}
		project, err := api.getProjectByApiKey(apiKey)
		if err != nil {
			klog.Errorln(err)
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		if project == nil {
			klog.Warningln("no project found")
			http.Error(w, "no project found", http.StatusNotFound)
			return
		}
		h(w, r, project)
		return
	}
}

func (api *Api) Login(w http.ResponseWriter, r *http.Request) {
	var form forms.LoginForm
	if err := forms.ReadAndValidate(r, &form); err != nil {
		klog.Warningln("bad request:", err)
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	var sso SSOSettings
	if form.Action != "set_admin_password" {
		var err error
		sso, err = api.getSSOSettings()
		if err != nil {
			klog.Errorln(err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		if sso.Enabled && sso.ForceSSO && sso.Provider != "ldap" {
			http.Error(w, "Password login is disabled. Please use SSO.", http.StatusForbidden)
			return
		}
	}
	var userId int
	switch form.Action {
	case "set_admin_password":
		admin, err := api.db.DefaultAdminUserIsTheOnlyUser()
		if err != nil {
			klog.Errorln(err)
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		if admin == nil {
			http.Error(w, "The admin password has already been set.", http.StatusUnauthorized)
			return
		}
		err = api.db.ChangeUserPassword(admin.Id, db.AdminUserDefaultPassword, form.Password)
		if err != nil {
			klog.Errorln(err)
			switch {
			case errors.Is(err, db.ErrNotFound):
				http.Error(w, "User not found.", http.StatusNotFound)
			case errors.Is(err, db.ErrInvalid):
				http.Error(w, "Invalid old password.", http.StatusBadRequest)
			case errors.Is(err, db.ErrConflict):
				http.Error(w, "New password can't be the same as the old one.", http.StatusBadRequest)
			default:
				http.Error(w, "", http.StatusInternalServerError)
			}
			return
		}
		userId = admin.Id
	default:
		if sso.Enabled && sso.Provider == "ldap" {
			user, err := api.authLDAPUser(form.Email, form.Password, sso)
			if err == nil {
				userId = user.Id
				break
			}
			klog.Warningln("ldap authentication failed:", err)
			if sso.ForceSSO {
				http.Error(w, "Invalid LDAP username or password.", http.StatusNotFound)
				return
			}
		}
		id, err := api.db.AuthUser(form.Email, form.Password)
		if err != nil {
			klog.Errorln(err)
			if errors.Is(err, db.ErrNotFound) {
				http.Error(w, "Invalid email or password.", http.StatusNotFound)
			} else {
				http.Error(w, "", http.StatusInternalServerError)
			}
			return
		}
		userId = id
	}
	err := api.SetSessionCookie(w, userId, SessionCookieTTL)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
}

func (api *Api) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Path:     "/",
		HttpOnly: true,
	})
}

func (api *Api) SSOStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := api.getSSOSettings()
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	utils.WriteJson(w, struct {
		Enabled  bool   `json:"enabled"`
		ForceSSO bool   `json:"force_sso"`
		Provider string `json:"provider"`
	}{
		Enabled:  settings.Enabled,
		ForceSSO: settings.ForceSSO,
		Provider: settings.Provider,
	})
}

func (api *Api) SSOLogin(w http.ResponseWriter, r *http.Request) {
	settings, err := api.getSSOSettings()
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if !settings.Enabled {
		http.Error(w, "SSO is disabled", http.StatusNotFound)
		return
	}
	if settings.Provider != "oidc" {
		http.Error(w, "Only OIDC SSO is supported in this local build", http.StatusBadRequest)
		return
	}
	oauth2Config, _, err := api.oidcConfig(r.Context(), settings, api.oidcRedirectURL(r))
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "Invalid OIDC configuration", http.StatusBadRequest)
		return
	}
	state := ssoState{
		Nonce: utils.RandomString(16),
		Next:  sanitizeNextURL(r.URL.Query().Get("next")),
	}
	value, err := api.signJSON(state)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SSOStateCookieName,
		Value:    value,
		Path:     "/",
		Expires:  time.Now().Add(SSOStateCookieTTL),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, oauth2Config.AuthCodeURL(value, oidc.Nonce(state.Nonce)), http.StatusFound)
}

func (api *Api) SSOOIDCCallback(w http.ResponseWriter, r *http.Request) {
	settings, err := api.getSSOSettings()
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if !settings.Enabled || settings.Provider != "oidc" {
		http.Redirect(w, r, api.loginURL(r, "sso_error=disabled"), http.StatusFound)
		return
	}
	stateValue := r.URL.Query().Get("state")
	var state ssoState
	if err = api.verifySignedJSON(stateValue, &state); err != nil {
		klog.Warningln("invalid oidc state:", err)
		http.Redirect(w, r, api.loginURL(r, "sso_error=state"), http.StatusFound)
		return
	}
	cookie, _ := r.Cookie(SSOStateCookieName)
	if cookie == nil || cookie.Value != stateValue {
		klog.Warningln("missing oidc state cookie")
		http.Redirect(w, r, api.loginURL(r, "sso_error=state"), http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SSOStateCookieName, Path: "/", HttpOnly: true, MaxAge: -1})

	oauth2Config, verifier, err := api.oidcConfig(r.Context(), settings, api.oidcRedirectURL(r))
	if err != nil {
		klog.Errorln(err)
		http.Redirect(w, r, api.loginURL(r, "sso_error=configuration"), http.StatusFound)
		return
	}
	token, err := oauth2Config.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		klog.Warningln("oidc token exchange failed:", err)
		http.Redirect(w, r, api.loginURL(r, "sso_error=token"), http.StatusFound)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		klog.Warningln("oidc id_token is missing")
		http.Redirect(w, r, api.loginURL(r, "sso_error=token"), http.StatusFound)
		return
	}
	idToken, err := verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		klog.Warningln("oidc id_token verification failed:", err)
		http.Redirect(w, r, api.loginURL(r, "sso_error=token"), http.StatusFound)
		return
	}
	if idToken.Nonce != state.Nonce {
		klog.Warningln("oidc nonce mismatch")
		http.Redirect(w, r, api.loginURL(r, "sso_error=state"), http.StatusFound)
		return
	}
	var claims struct {
		Email      string `json:"email"`
		Name       string `json:"name"`
		GivenName  string `json:"given_name"`
		FamilyName string `json:"family_name"`
	}
	if err = idToken.Claims(&claims); err != nil {
		klog.Warningln("oidc claims parsing failed:", err)
		http.Redirect(w, r, api.loginURL(r, "sso_error=configuration"), http.StatusFound)
		return
	}
	claims.Email = strings.TrimSpace(claims.Email)
	if claims.Email == "" {
		klog.Warningln("oidc email claim is missing")
		http.Redirect(w, r, api.loginURL(r, "sso_error=configuration"), http.StatusFound)
		return
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = strings.TrimSpace(strings.Join([]string{claims.GivenName, claims.FamilyName}, " "))
	}
	user, err := api.db.GetOrCreateSSOUser(claims.Email, name, settings.DefaultRole)
	if err != nil {
		klog.Errorln(err)
		http.Redirect(w, r, api.loginURL(r, "sso_error=user"), http.StatusFound)
		return
	}
	if err = api.SetSessionCookie(w, user.Id, SessionCookieTTL); err != nil {
		klog.Errorln(err)
		http.Redirect(w, r, api.loginURL(r, "sso_error=session"), http.StatusFound)
		return
	}
	http.Redirect(w, r, state.Next, http.StatusFound)
}

func (api *Api) SetSessionCookie(w http.ResponseWriter, userId int, ttl time.Duration) error {
	data, err := json.Marshal(Session{Id: userId})
	if err != nil {
		return err
	}
	h := hmac.New(HashFunc.New, []byte(api.authSecret))
	h.Write(data)
	value := base64.URLEncoding.EncodeToString(data) + "." + base64.URLEncoding.EncodeToString(h.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  time.Now().Add(ttl),
		HttpOnly: true,
	})
	return nil
}

func (api *Api) GetUser(r *http.Request) *db.User {
	if api.authAnonymousRole != "" {
		return db.AnonymousUser(api.authAnonymousRole)
	}

	c, _ := r.Cookie(SessionCookieName)
	if c == nil {
		return nil
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		klog.Errorln("invalid session")
		return nil
	}
	data, err := base64.URLEncoding.DecodeString(parts[0])
	if err != nil {
		klog.Errorln(err)
		return nil
	}
	h := hmac.New(HashFunc.New, []byte(api.authSecret))
	h.Write(data)
	if parts[1] != base64.URLEncoding.EncodeToString(h.Sum(nil)) {
		klog.Errorln("invalid session")
		return nil
	}
	var sess Session
	err = json.Unmarshal(data, &sess)
	if err != nil {
		klog.Errorln(err)
		return nil
	}

	user, err := api.db.GetUser(sess.Id)
	if err != nil {
		klog.Errorln(err)
		return nil
	}
	return user
}

func (api *Api) getSSOSettings() (SSOSettings, error) {
	settings := SSOSettings{
		Provider:    "oidc",
		DefaultRole: rbac.RoleViewer,
	}
	err := api.db.GetSetting(SSOSettingName, &settings)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return settings, nil
		}
		return settings, err
	}
	if settings.Provider == "" {
		settings.Provider = "oidc"
	}
	if settings.DefaultRole == "" {
		settings.DefaultRole = rbac.RoleViewer
	}
	return settings, nil
}

func (api *Api) oidcConfig(ctx context.Context, settings SSOSettings, redirectURL string) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	provider, err := oidc.NewProvider(ctx, settings.OIDC.IssuerURL)
	if err != nil {
		return nil, nil, err
	}
	config := &oauth2.Config{
		ClientID:     settings.OIDC.ClientID,
		ClientSecret: settings.OIDC.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return config, provider.Verifier(&oidc.Config{ClientID: settings.OIDC.ClientID}), nil
}

func (api *Api) authLDAPUser(username, password string, settings SSOSettings) (*db.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, db.ErrNotFound
	}
	cfg := settings.LDAP
	if cfg.UserFilter == "" {
		cfg.UserFilter = "(uid={username})"
	}
	if cfg.EmailAttribute == "" {
		cfg.EmailAttribute = "mail"
	}
	if cfg.NameAttribute == "" {
		cfg.NameAttribute = "cn"
	}

	conn, err := ldap.DialURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if cfg.StartTLS {
		if err = conn.StartTLS(&tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}); err != nil {
			return nil, err
		}
	}
	if cfg.BindDN != "" {
		if err = conn.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			return nil, err
		}
	}

	filter := strings.ReplaceAll(cfg.UserFilter, "{username}", ldap.EscapeFilter(username))
	filter = strings.ReplaceAll(filter, "{email}", ldap.EscapeFilter(username))
	search := ldap.NewSearchRequest(
		cfg.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1,
		0,
		false,
		filter,
		[]string{cfg.EmailAttribute, cfg.NameAttribute},
		nil,
	)
	result, err := conn.Search(search)
	if err != nil {
		return nil, err
	}
	if len(result.Entries) != 1 {
		return nil, db.ErrNotFound
	}
	entry := result.Entries[0]
	if err = conn.Bind(entry.DN, password); err != nil {
		return nil, db.ErrNotFound
	}
	email := strings.TrimSpace(entry.GetAttributeValue(cfg.EmailAttribute))
	if email == "" {
		email = username
	}
	name := strings.TrimSpace(entry.GetAttributeValue(cfg.NameAttribute))
	return api.db.GetOrCreateSSOUser(email, name, settings.DefaultRole)
}

func (api *Api) oidcRedirectURL(r *http.Request) string {
	return externalBaseURL(r) + "sso/oidc"
}

func externalBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	prefix := r.Header.Get("X-Forwarded-Prefix")
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return scheme + "://" + host + prefix
}

func sanitizeNextURL(next string) string {
	if next == "" {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || !strings.HasPrefix(next, "/") {
		return "/"
	}
	return next
}

func (api *Api) loginURL(r *http.Request, query string) string {
	u := externalBaseURL(r) + "login"
	if query != "" {
		u += "?" + query
	}
	return u
}

func (api *Api) signJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := hmac.New(HashFunc.New, []byte(api.authSecret))
	h.Write(data)
	return base64.URLEncoding.EncodeToString(data) + "." + base64.URLEncoding.EncodeToString(h.Sum(nil)), nil
}

func (api *Api) verifySignedJSON(value string, v any) error {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return fmt.Errorf("invalid signed value")
	}
	data, err := base64.URLEncoding.DecodeString(parts[0])
	if err != nil {
		return err
	}
	h := hmac.New(HashFunc.New, []byte(api.authSecret))
	h.Write(data)
	if parts[1] != base64.URLEncoding.EncodeToString(h.Sum(nil)) {
		return fmt.Errorf("invalid signature")
	}
	return json.Unmarshal(data, v)
}

func (api *Api) IsAllowed(u *db.User, actions ...rbac.Action) bool {
	roles, err := api.roles.GetRoles()
	if err != nil {
		klog.Errorln(err)
		return false
	}

	for _, rn := range u.Roles {
		for _, r := range roles {
			if r.Name != rn {
				continue
			}
			for _, action := range actions {
				if r.Permissions.Allows(action) {
					return true
				}
			}
		}
	}
	return false
}
