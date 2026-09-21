package adapter

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Paginate pages an in-memory list for backends whose upstream has no
// server-side paging. Items are ordered by key first, because an offset
// cursor is only meaningful over a stable order and upstream listings give
// none; key is also the text Search matches against.
func Paginate[T any](items []T, q ListQuery, key func(T) string) (Page[T], error) {
	offset := 0
	if q.Cursor != "" {
		n, err := strconv.Atoi(q.Cursor)
		if err != nil || n < 0 {
			return Page[T]{}, &Error{Kind: Invalid, Op: "page", Err: errors.New("invalid cursor")}
		}
		offset = n
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	filtered := make([]T, 0, len(items))
	needle := strings.ToLower(q.Search)
	for _, item := range items {
		if needle == "" || strings.Contains(strings.ToLower(key(item)), needle) {
			filtered = append(filtered, item)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return key(filtered[i]) < key(filtered[j]) })

	total := len(filtered)
	page := Page[T]{Items: []T{}, Total: &total}
	if offset >= total {
		return page, nil
	}
	end := offset + limit
	if end < total {
		page.Next = strconv.Itoa(end)
	} else {
		end = total
	}
	page.Items = filtered[offset:end]
	return page, nil
}

// SplitMXID separates @localpart:server into its parts; both are empty when
// id is not an MXID.
func SplitMXID(id string) (localpart, domain string) {
	if !strings.HasPrefix(id, "@") {
		return "", ""
	}
	localpart, domain, ok := strings.Cut(id[1:], ":")
	if !ok || localpart == "" || domain == "" {
		return "", ""
	}
	return localpart, domain
}

// SplitJID separates a bare or full JID into localpart, domain and resource.
func SplitJID(jid string) (localpart, domain, resource string) {
	bare, resource, _ := strings.Cut(jid, "/")
	localpart, domain, hasAt := strings.Cut(bare, "@")
	if !hasAt {
		return "", bare, resource
	}
	return localpart, domain, resource
}
