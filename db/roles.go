package db

import (
	"errors"
	"slices"

	"github.com/coroot/coroot/rbac"
)

const CustomRolesSettingName = "custom_roles"

func (db *DB) GetRoles() ([]rbac.Role, error) {
	var custom []rbac.Role
	err := db.GetSetting(CustomRolesSettingName, &custom)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	roles := append([]rbac.Role{}, rbac.Roles...)
	for _, role := range custom {
		if role.Name == "" || role.Name.Builtin() {
			continue
		}
		roles = append(roles, role)
	}
	return roles, nil
}

func (db *DB) GetCustomRoles() ([]rbac.Role, error) {
	var roles []rbac.Role
	err := db.GetSetting(CustomRolesSettingName, &roles)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return roles, nil
}

func (db *DB) SaveCustomRole(role rbac.Role, oldName rbac.RoleName) error {
	roles, err := db.GetCustomRoles()
	if err != nil {
		return err
	}
	if oldName == "" {
		oldName = role.Name
	}
	replaced := false
	for i, r := range roles {
		if r.Name == oldName {
			roles[i] = role
			replaced = true
			continue
		}
		if r.Name == role.Name && oldName != role.Name {
			return ErrConflict
		}
	}
	if !replaced {
		if slices.ContainsFunc(roles, func(r rbac.Role) bool { return r.Name == role.Name }) {
			return ErrConflict
		}
		roles = append(roles, role)
	}
	return db.SetSetting(CustomRolesSettingName, roles)
}

func (db *DB) DeleteCustomRole(name rbac.RoleName) error {
	roles, err := db.GetCustomRoles()
	if err != nil {
		return err
	}
	filtered := roles[:0]
	for _, role := range roles {
		if role.Name != name {
			filtered = append(filtered, role)
		}
	}
	return db.SetSetting(CustomRolesSettingName, filtered)
}
