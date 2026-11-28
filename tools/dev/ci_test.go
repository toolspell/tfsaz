package main

import (
	"bytes"
	"path/filepath"
	"slices"
	"testing"
)

func TestShardTests(t *testing.T) {
	out := "TestB\nExampleA\nBenchmarkX\nTestUTF8_é\nok  \tgithub.com/toolspell/tfsaz\t0.1s\nTestA\nFuzzC\nTestB\n?   \tgithub.com/toolspell/tfsaz/tools/x\t[no test files]\nok  \tgithub.com/toolspell/tfsaz/internal/az\t0.2s\n"
	names := listedTests(out)
	if want := []string{"ExampleA", "FuzzC", "TestA", "TestB", "TestUTF8_é"}; !slices.Equal(names, want) {
		t.Fatalf("listedTests = %q, want %q", names, want)
	}
	for total := 1; total <= len(names)+1; total++ {
		var all []string
		for index := 1; index <= total; index++ {
			all = append(all, shardTests(names, index, total)...)
		}
		slices.Sort(all)
		if !slices.Equal(all, names) {
			t.Errorf("%d shards run %q, want each of %q once", total, all, names)
		}
	}
	for _, tc := range []struct {
		shard        string
		index, total int
		ok           bool
	}{
		{"1/1", 1, 1, true}, {"3/3", 3, 3, true}, {"0/3", 0, 0, false}, {"4/3", 0, 0, false}, {"1/0", 0, 0, false}, {"2", 0, 0, false}, {"a/b", 0, 0, false},
	} {
		index, total, err := parseShard(tc.shard)
		if index != tc.index || total != tc.total || (err == nil) != tc.ok {
			t.Errorf("parseShard(%q) = %d, %d, %v", tc.shard, index, total, err)
		}
	}
}

func TestDeadcodeGate(t *testing.T) {
	t.Chdir("../..")
	dir := filepath.Join("tools", "dev", "testdata", "deadcode")
	var out bytes.Buffer
	if err := toolFor(dir, "", "deadcode", "-json", "./...")(&out); err != nil {
		t.Fatalf("deadcode: %v\n%s", err, out.String())
	}
	dead, err := unallowedDeadcode(dir, out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tools/dev/testdata/deadcode/main.go:9: unmarked", "tools/dev/testdata/deadcode/other/other.go:5: Dead"}
	if !slices.Equal(dead, want) {
		t.Errorf("unallowed dead code %q, want %q: the marked function and the marked package pass; a marker in an external test package covers nothing", dead, want)
	}
}
