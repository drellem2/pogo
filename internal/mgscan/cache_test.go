package mgscan

import "testing"

func TestCacheHitsOnlyAtTheSameMtime(t *testing.T) {
	c := NewCache[string]()
	c.Store("mg-1", "t1", "a")
	if v, ok := c.Lookup("mg-1", "t1"); !ok || v != "a" {
		t.Fatalf("Lookup at the stored mtime = %q, %v; want a, true", v, ok)
	}
	if _, ok := c.Lookup("mg-1", "t2"); ok {
		t.Fatal("a moved mtime must miss — that is the whole invalidation rule")
	}
	if _, ok := c.Lookup("mg-2", "t1"); ok {
		t.Fatal("an unknown id must miss")
	}
	c.Store("mg-1", "t2", "b")
	if v, ok := c.Lookup("mg-1", "t2"); !ok || v != "b" {
		t.Fatalf("after re-store Lookup = %q, %v; want b, true", v, ok)
	}
}

func TestCacheRefusesAnEmptyMtime(t *testing.T) {
	c := NewCache[string]()
	c.Store("mg-1", "", "a")
	if c.Len() != 0 {
		t.Fatal("a row with no mtime cannot be validated and must not be cached")
	}
	if _, ok := c.Lookup("mg-1", ""); ok {
		t.Fatal("an empty mtime must always miss")
	}
}

func TestNilCacheCachesNothing(t *testing.T) {
	var c *Cache[int]
	c.Store("mg-1", "t1", 1)
	if _, ok := c.Lookup("mg-1", "t1"); ok {
		t.Fatal("a nil cache must miss")
	}
	c.Retain(nil)
	c.Forget("mg-1")
	if c.Len() != 0 {
		t.Fatal("a nil cache has no entries")
	}
}

func TestCacheRetainDropsIdsThatLeftTheListing(t *testing.T) {
	c := NewCache[int]()
	c.Store("keep", "t", 1)
	c.Store("gone", "t", 2)
	c.Retain(map[string]bool{"keep": true})
	if _, ok := c.Lookup("keep", "t"); !ok {
		t.Error("a listed id must be retained")
	}
	if _, ok := c.Lookup("gone", "t"); ok {
		t.Error("an id missing from the listing must be dropped")
	}
	c.Forget("keep")
	if c.Len() != 0 {
		t.Errorf("Len after Forget = %d, want 0", c.Len())
	}
}

func TestParseList(t *testing.T) {
	rows, err := ParseList([]byte(`{"id":"mg-1","status":"done","mtime":"2026-09-26T09:58:36.295296876+01:00","title":"x"}

{"id":"mg-2","status":"archived"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0] != (Row{"mg-1", "done", "2026-09-26T09:58:36.295296876+01:00"}) ||
		rows[1] != (Row{ID: "mg-2", Status: "archived"}) {
		t.Fatalf("ParseList = %+v", rows)
	}
	if _, err := ParseList([]byte("not json\n")); err == nil {
		t.Fatal("an unparseable row must be an error, not a silently shorter store")
	}
}
