package main

import "testing"

func TestServedPagePrefersNamespace(t *testing.T) {
	old := &sitePage{Source: "main/P/Portal%3ALocations.md", Meta: pageMeta{Title: "Portal:Locations", Namespace: 0, Revision: 900000}}
	portal := &sitePage{Source: "portal/L/Locations.md", Meta: pageMeta{Title: "Portal:Locations", Namespace: 104, Revision: 421730}}
	for _, pair := range [][2]*sitePage{{old, portal}, {portal, old}} {
		if keep, drop := servedPage(pair[0], pair[1]); keep != portal || drop != old {
			t.Fatalf("kept %s, want the Portal namespace page", keep.Source)
		}
	}
	a := &sitePage{Meta: pageMeta{Title: "X", Revision: 1}}
	b := &sitePage{Meta: pageMeta{Title: "X", Revision: 2}}
	if keep, _ := servedPage(a, b); keep != b {
		t.Fatal("did not keep the later revision")
	}
}
