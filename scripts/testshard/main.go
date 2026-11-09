// testshard runs complete, deterministic groups from a compiled Go test binary.
// Discovery uses the binary's actual registry, including examples and fuzz seeds.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

type options struct {
	parts, index int
	race         bool
	filter       string
	firstOnly    string
	packages     []string
}

type testPlan struct {
	dir, binary, pkg string
	groups           [][]string
}

func groupedNames(output, filter, firstOnly string, parts int) ([][]string, bool, error) {
	if parts < 1 || parts > 16 || strings.Contains(filter, "/") {
		return nil, false, errors.New("use 1–16 groups and a top-level test regexp")
	}
	match, err := regexp.Compile(filter)
	if err != nil {
		return nil, false, err
	}
	names, seen := []string{}, map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "Benchmark") || name == "" {
			continue // go test does not run benchmarks by default.
		}
		if !strings.HasPrefix(name, "Test") && !strings.HasPrefix(name, "Example") && !strings.HasPrefix(name, "Fuzz") || strings.ContainsAny(name, " \t\r") || seen[name] {
			return nil, false, fmt.Errorf("invalid or duplicate test registry entry %q", name)
		}
		seen[name] = true
		if match.MatchString(name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, false, errors.New("no registered tests match the requested scope")
	}
	sort.Strings(names)
	groups, pinned := make([][]string, parts), false
	for _, name := range names {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(name))
		index := int(hash.Sum64() % uint64(parts))
		if firstOnly != "" && name == firstOnly {
			index, pinned = 0, true
		}
		groups[index] = append(groups[index], name)
	}
	return groups, pinned, nil
}

func execute(ctx context.Context, dir string, stdout, stderr io.Writer, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, stdout, stderr
	return cmd.Run()
}

func run(ctx context.Context, o options, stdout, stderr io.Writer) error {
	if o.parts < 1 || o.parts > 16 || o.index < -1 || o.index >= o.parts || len(o.packages) == 0 {
		return errors.New("supply packages, 1–16 groups, and index -1 (all) or an actual group")
	}
	root, err := os.MkdirTemp("", "mantik-testshard-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	plans, pinned := []testPlan{}, false
	for i, pkg := range o.packages {
		var listed bytes.Buffer
		if err := execute(ctx, "", &listed, stderr, "go", "list", "-json", pkg); err != nil {
			return err
		}
		var info struct{ Dir, ImportPath string }
		decoder := json.NewDecoder(&listed)
		if err := decoder.Decode(&info); err != nil || info.Dir == "" || info.ImportPath == "" {
			return fmt.Errorf("cannot identify package %s: %v", pkg, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return errors.New("each package argument must identify one package")
		}
		binary := filepath.Join(root, fmt.Sprintf("group-%d.test", i))
		args := []string{"test", "-c", "-o", binary}
		if o.race {
			args = append(args, "-race")
		}
		if err := execute(ctx, "", stdout, stderr, "go", append(args, pkg)...); err != nil {
			return err
		}
		var inventory bytes.Buffer
		if err := execute(ctx, info.Dir, &inventory, stderr, binary, "-test.list=."); err != nil {
			return err
		}
		groups, found, err := groupedNames(inventory.String(), o.filter, o.firstOnly, o.parts)
		if err != nil {
			return err
		}
		pinned = pinned || found
		plans = append(plans, testPlan{info.Dir, binary, info.ImportPath, groups})
	}
	if o.firstOnly != "" && !pinned {
		return fmt.Errorf("required first-group test %s is absent from this scope", o.firstOnly)
	}
	for _, plan := range plans {
		all := []string{}
		for _, group := range plan.groups {
			all = append(all, group...)
		}
		sort.Strings(all)
		inventorySHA := sha256.Sum256([]byte(strings.Join(all, "\n")))
		for index, group := range plan.groups {
			if o.index != -1 && index != o.index {
				continue
			}
			selectedSHA := sha256.Sum256([]byte(strings.Join(group, "\n")))
			if err := json.NewEncoder(stdout).Encode(struct {
				Package         string `json:"package"`
				Group           int    `json:"group"`
				Groups          int    `json:"groups"`
				Selected        int    `json:"selected"`
				Total           int    `json:"total"`
				InventorySHA256 string `json:"inventory_sha256"`
				SelectedSHA256  string `json:"selected_sha256"`
				Race            bool   `json:"race"`
			}{plan.pkg, index, o.parts, len(group), len(all), hex.EncodeToString(inventorySHA[:]), hex.EncodeToString(selectedSHA[:]), o.race}); err != nil {
				return err
			}
			if len(group) == 0 {
				continue
			}
			escaped := make([]string, len(group))
			for i, name := range group {
				escaped[i] = regexp.QuoteMeta(name)
			}
			pattern := "^(" + strings.Join(escaped, "|") + ")$"
			if err := execute(ctx, plan.dir, stdout, stderr, plan.binary, "-test.run="+pattern, "-test.count=1", "-test.timeout=30m", "-test.v=true"); err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	var o options
	flag.IntVar(&o.parts, "parts", 2, "number of deterministic test groups, 1–16")
	flag.IntVar(&o.index, "index", -1, "zero-based group index; -1 runs every group")
	flag.BoolVar(&o.race, "race", false, "compile with the Go race detector")
	flag.StringVar(&o.filter, "run", ".", "regexp selecting complete top-level tests/examples/fuzz seeds")
	flag.StringVar(&o.firstOnly, "first-only", "", "required test to place in group zero, such as the recovery seed")
	flag.Parse()
	o.packages = flag.Args()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
