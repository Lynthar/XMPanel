package models

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// The Go constants and struct tags in this package are the source of truth;
// the frontend tables and locale files are copies of them. These tests read
// every copy off disk so adding an entry on one side without the others fails here.

const webDir = "../../../web/src"

func constValues(t *testing.T, file, typeName string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var values []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if ident, ok := vs.Type.(*ast.Ident); !ok || ident.Name != typeName {
				continue
			}
			for _, v := range vs.Values {
				values = append(values, strings.Trim(v.(*ast.BasicLit).Value, `"`))
			}
		}
	}
	return values
}

func readWeb(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(webDir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// block returns the text between the first line containing start and the next
// line that closes it with end.
func block(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("%q not found", start)
	}
	j := strings.Index(src[i:], "\n"+end)
	if j < 0 {
		t.Fatalf("no %q after %q", end, start)
	}
	return src[i : i+j]
}

// matches returns the first capture group of every match of pattern in src.
func matches(pattern, src string) []string {
	var out []string
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

func unique(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func localeKeys(t *testing.T, file string, path ...string) []string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readWeb(t, "i18n/locales/"+file)), &doc); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, p := range path {
		next := map[string]json.RawMessage{}
		if err := json.Unmarshal(doc[p], &next); err != nil {
			t.Fatalf("%s: no object at %q: %v", file, p, err)
		}
		doc = next
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	return keys
}

func sameSet(t *testing.T, wantName string, want []string, gotName string, got []string) {
	t.Helper()
	w, g := append([]string(nil), want...), append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	if !reflect.DeepEqual(w, g) {
		t.Errorf("%s and %s disagree:\n  only in %s: %v\n  only in %s: %v",
			wantName, gotName, wantName, minus(w, g), gotName, minus(g, w))
	}
}

func minus(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

func TestAuditActionsMatchEveryCopy(t *testing.T) {
	actions := constValues(t, "audit.go", "AuditAction")
	resources := constValues(t, "audit.go", "ResourceType")
	if len(actions) == 0 || len(resources) == 0 {
		t.Fatal("no constants found")
	}
	page := readWeb(t, "pages/AuditLogs.tsx")
	sameSet(t, "AuditAction", actions, "AuditLogs.tsx actionColors", matches(`'([^']+)':`, block(t, page, "const actionColors", "}")))
	sameSet(t, "ResourceType", resources, "AuditLogs.tsx RESOURCE_TYPE_OPTIONS", matches(`'([^']+)'`, block(t, page, "const RESOURCE_TYPE_OPTIONS", "\n")))
	for _, file := range []string{"en.json", "zh.json"} {
		sameSet(t, "AuditAction", actions, file+" audit.actions", localeKeys(t, file, "audit", "actions"))
		sameSet(t, "ResourceType", resources, file+" audit.resourceTypes", localeKeys(t, file, "audit", "resourceTypes"))
	}
}

// The role forms deliberately never offer superadmin: that grant stays API-only.
func TestRolesMatchEveryCopy(t *testing.T) {
	roles := constValues(t, "user.go", "Role")
	page := readWeb(t, "pages/Users.tsx")

	sameSet(t, "Role", roles, "Users.tsx roleColors", matches(`(?m)^\s+(\w+): 'badge-`, block(t, page, "const roleColors", "  }")))
	for _, file := range []string{"en.json", "zh.json"} {
		sameSet(t, "Role", roles, file+" users.roles", localeKeys(t, file, "users", "roles"))
	}

	offered := matches(`<option value="(\w+)">\{t\('users\.roles\.`, page)
	offerable := minus(roles, []string{string(RoleSuperAdmin)})
	if len(offered) != 2*len(offerable) {
		t.Errorf("expected two role selects offering %d roles each, found %d options", len(offerable), len(offered))
	}
	sameSet(t, "Role minus superadmin", offerable, "Users.tsx role <select> options", unique(offered))
}

// tsFields lists an `export interface` body of lib/api.ts as name or name? per line.
func tsFields(t *testing.T, name string) []string {
	t.Helper()
	return matches(`(?m)^\s+(\w+\??):`, block(t, readWeb(t, "lib/api.ts"), "export interface "+name+" {", "}"))
}

// jsonFields lists a struct's wire fields the same way: json name, plus ? when omitempty.
func jsonFields(v interface{}) []string {
	var fields []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")
		if tag[0] == "-" || tag[0] == "" {
			continue
		}
		name := tag[0]
		if len(tag) > 1 && tag[1] == "omitempty" {
			name += "?"
		}
		fields = append(fields, name)
	}
	return fields
}

func TestWireTypesMatchFrontend(t *testing.T) {
	sameSet(t, "models.User json", jsonFields(User{}), "lib/api.ts User", tsFields(t, "User"))
	sameSet(t, "models.Server json", jsonFields(Server{}), "lib/api.ts Server", tsFields(t, "Server"))
	sameSet(t, "adapter.Account json", jsonFields(adapter.Account{}), "lib/api.ts Account", tsFields(t, "Account"))
	sameSet(t, "adapter.XMPPAccountFacts json", jsonFields(adapter.XMPPAccountFacts{}), "lib/api.ts XMPPAccountFacts", tsFields(t, "XMPPAccountFacts"))
	sameSet(t, "adapter.Session json", jsonFields(adapter.Session{}), "lib/api.ts Session", tsFields(t, "Session"))
	sameSet(t, "adapter.XMPPSessionFacts json", jsonFields(adapter.XMPPSessionFacts{}), "lib/api.ts XMPPSessionFacts", tsFields(t, "XMPPSessionFacts"))
	sameSet(t, "adapter.Room json", jsonFields(adapter.Room{}), "lib/api.ts Room", tsFields(t, "Room"))
	sameSet(t, "adapter.XMPPRoomFacts json", jsonFields(adapter.XMPPRoomFacts{}), "lib/api.ts XMPPRoomFacts", tsFields(t, "XMPPRoomFacts"))
	sameSet(t, "adapter.Stats json", jsonFields(adapter.Stats{}), "lib/api.ts Stats", tsFields(t, "Stats"))
	sameSet(t, "adapter.ServerInfo json", jsonFields(adapter.ServerInfo{}), "lib/api.ts ServerInfo", tsFields(t, "ServerInfo"))
	sameSet(t, "adapter.CreateAccount json", jsonFields(adapter.CreateAccount{}), "lib/api.ts CreateAccountRequest", tsFields(t, "CreateAccountRequest"))
	sameSet(t, "adapter.CreateRoom json", jsonFields(adapter.CreateRoom{}), "lib/api.ts CreateRoomRequest", tsFields(t, "CreateRoomRequest"))
	sameSet(t, "models.CreateServerRequest json", jsonFields(CreateServerRequest{}), "lib/api.ts CreateServerRequest", tsFields(t, "CreateServerRequest"))
}

// The implementation table and the capability list are the only protocol
// facts the frontend hard-codes; both are copies of Go constants.
func TestImplementationsAndCapabilitiesMatchFrontend(t *testing.T) {
	impls := constValues(t, "../../adapter/types.go", "Implementation")
	labels := block(t, readWeb(t, "lib/api.ts"), "export const implementations", "}")
	sameSet(t, "adapter.Implementation", impls, "lib/api.ts implementations", matches(`(?m)^\s+'?([\w-]+)'?:`, labels))

	caps := make([]string, 0, len(adapter.AllCapabilities))
	for _, c := range adapter.AllCapabilities {
		caps = append(caps, string(c))
	}
	sameSet(t, "adapter.AllCapabilities", caps, "lib/api.ts Capability", matches(`'([a-z_.]+)'`, block(t, readWeb(t, "lib/api.ts"), "export type Capability =", "\n")))
	for _, file := range []string{"en.json", "zh.json"} {
		sameSet(t, "adapter.Implementation", impls, file+" servers.implementations", localeKeys(t, file, "servers", "implementations"))
	}
}
