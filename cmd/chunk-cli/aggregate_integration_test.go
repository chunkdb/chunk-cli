package main

import "testing"

func TestCLIAggregateReplies(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (h i64, u u64 NULL, d f64 NULL) CHUNK 2 x 2")
	s.ok(t, "SET BLOCK 0 0 IN world h = 9223372036854775807, u = 18446744073709551615")
	s.ok(t, "SET BLOCK 1 0 IN world h = 9223372036854775807, d = 2.5")
	s.ok(t, "SET BLOCK 2 2 IN world h = 1")
	const total = "AGGREGATE AREA 0 0 TO 1 1 FROM world COUNT(*), SUM(h), MAX(u), AVG(d)"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{total}, "count(*) = 3\nsum(h) = 18446744073709551615\nmax(u) = 18446744073709551615\navg(d) = 2.5\n"},
		{[]string{"--json", total}, `{"count(*)":3,"sum(h)":"18446744073709551615","max(u)":"18446744073709551615","avg(d)":2.5}` + "\n"},
		{[]string{"AGGREGATE AREA 0 0 TO 1 1 FROM world COUNT(*), SUM(h), MAX(u) BY CHUNK"}, "1) [0, 0, 2, 18446744073709551614, 18446744073709551615]\n2) [1, 1, 1, 1, NULL]\n"},
		{[]string{"--json", "AGGREGATE AREA 0 0 TO 1 1 FROM world COUNT(*), SUM(h), MAX(u) BY CHUNK"}, `[[0,0,2,"18446744073709551614","18446744073709551615"],[1,1,1,1,null]]` + "\n"},
		{[]string{"AGGREGATE CHUNK 5 5 FROM world COUNT(*), SUM(h)"}, "count(*) = 0\nsum(h) = NULL\n"},
		{[]string{"AGGREGATE CHUNK 5 5 FROM world COUNT(*) BY CHUNK"}, "(empty)\n"},
		{[]string{"--json", "AGGREGATE CHUNK 5 5 FROM world COUNT(*) BY CHUNK"}, "[]\n"},
	} {
		if got := s.ok(t, c.args...); got != c.want {
			t.Errorf("%v:\ngot  %q\nwant %q", c.args, got, c.want)
		}
	}
}
