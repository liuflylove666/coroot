package forms

import (
	"strings"

	"github.com/coroot/coroot/rbac"
)

type LoginForm struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Action   string `json:"action"`
}

func (f *LoginForm) Valid() bool {
	return f.Email != "" && f.Password != ""
}

type ChangePasswordForm struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (f *ChangePasswordForm) Valid() bool {
	return f.OldPassword != "" && f.NewPassword != ""
}

type SSOForm struct {
	Action      string        `json:"action"`
	Provider    string        `json:"provider"`
	DefaultRole rbac.RoleName `json:"default_role"`
	ForceSSO    bool          `json:"force_sso"`
	SAML        *SAMLForm     `json:"saml"`
	OIDC        *OIDCForm     `json:"oidc"`
	LDAP        *LDAPForm     `json:"ldap"`
}

type SAMLForm struct {
	Metadata string `json:"metadata"`
}

type OIDCForm struct {
	IssuerURL    string `json:"issuer_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type LDAPForm struct {
	URL                string `json:"url"`
	StartTLS           bool   `json:"start_tls"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	BindDN             string `json:"bind_dn"`
	BindPassword       string `json:"bind_password"`
	BaseDN             string `json:"base_dn"`
	UserFilter         string `json:"user_filter"`
	EmailAttribute     string `json:"email_attribute"`
	NameAttribute      string `json:"name_attribute"`
}

func (f *SSOForm) Valid() bool {
	switch f.Action {
	case "disable":
		return true
	case "save", "upload":
	default:
		return false
	}
	f.Provider = strings.TrimSpace(f.Provider)
	if f.Provider == "" {
		f.Provider = "oidc"
	}
	if f.Provider != "oidc" && f.Provider != "saml" && f.Provider != "ldap" {
		return false
	}
	if f.Action == "upload" {
		return f.Provider == "saml" && f.SAML != nil && strings.TrimSpace(f.SAML.Metadata) != ""
	}
	return true
}

type UserAction string

const (
	UserActionCreate UserAction = "create"
	UserActionUpdate UserAction = "update"
	UserActionDelete UserAction = "delete"
)

type UserForm struct {
	Action   UserAction    `json:"action"`
	Id       int           `json:"id"`
	Email    string        `json:"email"`
	Name     string        `json:"name"`
	Role     rbac.RoleName `json:"role"`
	Password string        `json:"password"`
}

func (f *UserForm) Valid() bool {
	if f.Action == UserActionDelete {
		return true
	}
	f.Email = strings.TrimSpace(f.Email)
	f.Name = strings.TrimSpace(f.Name)
	return f.Email != "" && f.Name != ""
}

type RoleFormAction string

const (
	RoleFormActionAdd    RoleFormAction = "add"
	RoleFormActionEdit   RoleFormAction = "edit"
	RoleFormActionDelete RoleFormAction = "delete"
)

type RoleForm struct {
	Action      RoleFormAction       `json:"action"`
	Id          rbac.RoleName        `json:"id"`
	Name        rbac.RoleName        `json:"name"`
	Permissions []RolePermissionForm `json:"permissions"`
}

type RolePermissionForm struct {
	Scope  rbac.Scope `json:"scope"`
	Action rbac.Verb  `json:"action"`
	Object string     `json:"object"`
}

func (f *RoleForm) Valid() bool {
	switch f.Action {
	case RoleFormActionAdd, RoleFormActionEdit:
	case RoleFormActionDelete:
		return strings.TrimSpace(string(f.Id)) != ""
	default:
		return false
	}
	f.Name = rbac.RoleName(strings.TrimSpace(string(f.Name)))
	if f.Name == "" || f.Name.Builtin() {
		return false
	}
	for _, p := range f.Permissions {
		if p.Scope == "" || p.Action == "" {
			return false
		}
	}
	return true
}
