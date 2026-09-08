package models

import "testing"

// Every declared role must appear in the permission table. One that doesn't
// passes IsValid nowhere and grants nothing, so an account holding it can
// reach no endpoint at all.
func TestRoleIsValid_CoversEveryDeclaredRole(t *testing.T) {
	declared := []Role{RoleSuperAdmin, RoleAdmin, RoleOperator, RoleViewer, RoleAuditor}
	for _, role := range declared {
		if !role.IsValid() {
			t.Errorf("%q is declared but missing from Permissions", role)
		}
	}
	if len(Permissions) != len(declared) {
		t.Errorf("Permissions has %d roles, %d are declared as constants", len(Permissions), len(declared))
	}
}

func TestRoleIsValid_RejectsUnknown(t *testing.T) {
	for _, role := range []Role{"", "root", "Admin", "superadmin "} {
		if role.IsValid() {
			t.Errorf("%q accepted as a role", role)
		}
	}
}
