package handler

import (
	"errors"
	"testing"

	"github.com/xmpanel/xmpanel/internal/store/models"
)

func TestCheckRoleGrant(t *testing.T) {
	cases := []struct {
		name      string
		caller    models.Role
		requested models.Role
		want      error
	}{
		{"admin grants viewer", models.RoleAdmin, models.RoleViewer, nil},
		{"admin grants operator", models.RoleAdmin, models.RoleOperator, nil},
		{"admin grants auditor", models.RoleAdmin, models.RoleAuditor, nil},
		{"admin grants admin", models.RoleAdmin, models.RoleAdmin, nil},
		{"admin cannot grant superadmin", models.RoleAdmin, models.RoleSuperAdmin, errNeedSuperAdmin},
		{"superadmin grants superadmin", models.RoleSuperAdmin, models.RoleSuperAdmin, nil},
		{"invented role rejected", models.RoleSuperAdmin, models.Role("root"), errUnknownRole},
		{"empty role rejected", models.RoleAdmin, models.Role(""), errUnknownRole},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkRoleGrant(tc.caller, tc.requested); !errors.Is(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckTargetWritable(t *testing.T) {
	cases := []struct {
		name   string
		caller models.Role
		target models.Role
		want   error
	}{
		{"admin may touch a viewer", models.RoleAdmin, models.RoleViewer, nil},
		{"admin may touch another admin", models.RoleAdmin, models.RoleAdmin, nil},
		{"admin may not touch a superadmin", models.RoleAdmin, models.RoleSuperAdmin, errNeedSuperAdmin},
		{"superadmin may touch a superadmin", models.RoleSuperAdmin, models.RoleSuperAdmin, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkTargetWritable(tc.caller, tc.target); !errors.Is(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
