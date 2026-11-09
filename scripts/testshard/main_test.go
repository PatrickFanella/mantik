package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryPartitionIsCompleteAndDisjoint(t *testing.T) {
	registry := "TestAlpha\nTestBeta\nTestSeed\nExample\nFuzzCorpus\nBenchmarkNotRun\n"
	for parts := 1; parts <= 16; parts++ {
		groups, pinned, err := groupedNames(registry, ".", "TestSeed", parts)
		if err != nil || !pinned {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for index, group := range groups {
			for _, name := range group {
				seen[name]++
				if name == "TestSeed" && index != 0 {
					t.Fatal("recovery producer moved out of group zero")
				}
			}
		}
		if !reflect.DeepEqual(seen, map[string]int{"TestAlpha": 1, "TestBeta": 1, "TestSeed": 1, "Example": 1, "FuzzCorpus": 1}) {
			t.Fatal("test registry omitted or duplicated cases", seen)
		}
	}
	for _, registry := range []string{"TestAlpha\nTestAlpha", "unexpected init output", "BenchmarkOnly"} {
		if _, _, err := groupedNames(registry, ".", "", 2); err == nil {
			t.Fatal("invalid registry accepted", registry)
		}
	}
	if _, _, err := groupedNames("TestAlpha", "TestAlpha/subcase", "", 2); err == nil {
		t.Fatal("partial subtest selection accepted")
	}
}

func TestActualBinaryRunsEveryCaseOnceAndPropagatesFailure(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	files := map[string]string{
		"go.mod":        "module fixture.test/testshard\n\ngo 1.26.0\n",
		"marker":        "package directory",
		"race_test.go":  "//go:build race\n\npackage fixture\nconst instrumented = true\n",
		"plain_test.go": "//go:build !race\n\npackage fixture\nconst instrumented = false\n",
		"other/marker":  "other package directory",
		"other/fixture_test.go": `package other
import("fmt";"os";"testing")
func TestOther(t *testing.T) {if _,e:=os.ReadFile("marker");e!=nil{t.Fatal("wrong package directory",e)};f,e:=os.OpenFile(os.Getenv("TEST_SHARD_RECORD"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if e!=nil{t.Fatal(e)};defer f.Close();fmt.Fprintln(f,"TestOther")}
`,
		"fixture_test.go": `package fixture
import("fmt";"os";"testing")
func record(name string) { f,e:=os.OpenFile(os.Getenv("TEST_SHARD_RECORD"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if e!=nil{panic(e)};defer f.Close();fmt.Fprintln(f,name) }
func TestAlpha(t *testing.T) { if !instrumented {t.Fatal("race detector missing")};if _,e:=os.ReadFile("marker");e!=nil{t.Fatal("wrong package working directory",e)};if testing.Short(){t.Fatal("short tests enabled")};record("TestAlpha");t.Run("all-subcases",func(t *testing.T){record("subcase")}) }
func TestBeta(t *testing.T) {record("TestBeta")}
func TestSeed(t *testing.T) {record("TestSeed")}
func TestFailure(t *testing.T) {record("TestFailure");if os.Getenv("TEST_SHARD_FAIL")=="1"{t.Fatal("manufactured failure")}}
func FuzzCorpus(f *testing.F) {f.Add("seed");f.Fuzz(func(t *testing.T,s string){record("FuzzCorpus")})}
func Example() {record("Example");fmt.Println("ok")
// Output: ok
}
func BenchmarkNotRun(b *testing.B) {record("BenchmarkNotRun")}
`,
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	recording := filepath.Join(root, "cases.txt")
	t.Setenv("TEST_SHARD_RECORD", recording)
	var output bytes.Buffer
	o := options{parts: 2, index: -1, race: true, filter: ".", firstOnly: "TestSeed", packages: []string{"."}}
	if err := run(t.Context(), o, &output, &output); err != nil {
		t.Fatal(err, output.String())
	}
	raw, err := os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, name := range strings.Fields(string(raw)) {
		seen[name]++
	}
	want := map[string]int{"TestAlpha": 1, "subcase": 1, "TestBeta": 1, "TestSeed": 1, "TestFailure": 1, "FuzzCorpus": 1, "Example": 1}
	if !reflect.DeepEqual(seen, want) {
		t.Fatal("actual test binary skipped or repeated runnable cases", seen, output.String())
	}
	for _, dedicated := range []string{"", "TestAlpha"} {
		if err := os.Remove(recording); err != nil {
			t.Fatal(err)
		}
		o.dedicated = dedicated
		parts := 2
		if dedicated != "" {
			parts++
		}
		for index := range parts {
			o.index = index
			output.Reset()
			if err := run(t.Context(), o, &output, &output); err != nil {
				t.Fatal(err, output.String())
			}
			if dedicated != "" && index == 2 && !strings.Contains(output.String(), `"group":2,"groups":3,"selected":1`) {
				t.Fatal("dedicated group did not select exactly one complete case", output.String())
			}
		}
		raw, err = os.ReadFile(recording)
		if err != nil {
			t.Fatal(err)
		}
		seen = map[string]int{}
		for _, name := range strings.Fields(string(raw)) {
			seen[name]++
		}
		if !reflect.DeepEqual(seen, want) {
			t.Fatal("individual CI groups skipped or repeated runnable cases", dedicated, seen)
		}
	}
	if err := os.Remove(recording); err != nil {
		t.Fatal(err)
	}
	o.index, o.packages = -1, []string{".", "./other"}
	output.Reset()
	if err := run(t.Context(), o, &output, &output); err != nil {
		t.Fatal("mixed package scope", err, output.String())
	}
	raw, err = os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	seen = map[string]int{}
	for _, name := range strings.Fields(string(raw)) {
		seen[name]++
	}
	want["TestOther"] = 1
	if !reflect.DeepEqual(seen, want) || !strings.Contains(output.String(), `"package":"fixture.test/testshard/other","group":0,"groups":2`) {
		t.Fatal("dedicated group changed the other package's complete regular scope", seen, output.String())
	}
	o.dedicated, o.packages = "", []string{"."}
	o.index, o.firstOnly = -1, "TestMissingRecoveryProducer"
	output.Reset()
	if err := run(t.Context(), o, &output, &output); err == nil || !strings.Contains(err.Error(), "is absent") {
		t.Fatal("missing required recovery producer accepted", err, output.String())
	}
	o.firstOnly = "TestSeed"
	o.dedicated = "TestMissingDedicatedCase"
	output.Reset()
	if err := run(t.Context(), o, &output, &output); err == nil || !strings.Contains(err.Error(), "is absent") {
		t.Fatal("missing required dedicated test accepted", err, output.String())
	}
	o.dedicated = ""
	t.Setenv("TEST_SHARD_FAIL", "1")
	output.Reset()
	if err := run(t.Context(), o, &output, &output); err == nil || !strings.Contains(output.String(), "manufactured failure") {
		t.Fatal("test failure returned success", err, output.String())
	}
	o.dedicated, o.index = "TestFailure", 2
	output.Reset()
	if err := run(t.Context(), o, &output, &output); err == nil || !strings.Contains(output.String(), "manufactured failure") {
		t.Fatal("dedicated test failure returned success", err, output.String())
	}
}
