package adapter

import (
	"encoding/json"
	"testing"
)

func TestPaginateOrdersFiltersAndPages(t *testing.T) {
	items := []string{"carol@x", "alice@x", "bob@x", "dave@x"}
	key := func(s string) string { return s }

	page, err := Paginate(items, ListQuery{Limit: 3}, key)
	if err != nil || len(page.Items) != 3 || page.Next != "3" || *page.Total != 4 {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if page.Items[0] != "alice@x" || page.Items[2] != "carol@x" {
		t.Fatalf("items are not sorted: %v", page.Items)
	}
	page, err = Paginate(items, ListQuery{Limit: 3, Cursor: page.Next}, key)
	if err != nil || len(page.Items) != 1 || page.Items[0] != "dave@x" || page.Next != "" {
		t.Fatalf("last page = %+v, %v", page, err)
	}
	page, err = Paginate(items, ListQuery{Search: "AL"}, key)
	if err != nil || len(page.Items) != 1 || page.Items[0] != "alice@x" || *page.Total != 1 {
		t.Fatalf("search is not case-insensitive: %+v, %v", page, err)
	}
	page, err = Paginate(items, ListQuery{Cursor: "99"}, key)
	if err != nil || len(page.Items) != 0 || page.Next != "" {
		t.Fatalf("cursor past the end = %+v, %v", page, err)
	}
	if _, err := Paginate(items, ListQuery{Cursor: "x"}, key); !isKind(err, Invalid) {
		t.Fatalf("bad cursor = %v", err)
	}
	if _, err := Paginate(items, ListQuery{Cursor: "-1"}, key); !isKind(err, Invalid) {
		t.Fatalf("negative cursor = %v", err)
	}
	if raw, _ := json.Marshal(Page[string]{Items: []string{}}); string(raw) != `{"items":[]}` {
		t.Fatalf("empty page marshals as %s", raw)
	}
}

func TestSplitJID(t *testing.T) {
	for _, tc := range []struct{ in, local, domain, resource string }{
		{"alice@example.com/phone/one", "alice", "example.com", "phone/one"},
		{"alice@example.com", "alice", "example.com", ""},
		{"alice", "", "alice", ""},
		{"example.com/res", "", "example.com", "res"},
	} {
		local, domain, resource := SplitJID(tc.in)
		if local != tc.local || domain != tc.domain || resource != tc.resource {
			t.Errorf("SplitJID(%q) = %q %q %q", tc.in, local, domain, resource)
		}
	}
}

func TestCapabilitySetJSONIsSortedAndRoundTrips(t *testing.T) {
	set := NewCapabilitySet(CapRoomsList, CapAccountsList, CapAccountsCreate)
	raw, err := json.Marshal(set)
	if err != nil || string(raw) != `["accounts.create","accounts.list","rooms.list"]` {
		t.Fatalf("marshal = %s, %v", raw, err)
	}
	var back CapabilitySet
	if err := json.Unmarshal(raw, &back); err != nil || len(back) != 3 || !back.Has(CapRoomsList) {
		t.Fatalf("unmarshal = %v, %v", back, err)
	}
	if narrowed := set.Without(CapRoomsList); narrowed.Has(CapRoomsList) || !set.Has(CapRoomsList) {
		t.Fatal("Without must copy rather than mutate")
	}
}

func isKind(err error, kind Kind) bool {
	failure, ok := AsError(err)
	return ok && failure.Kind == kind
}
