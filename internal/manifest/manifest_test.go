// The declaration in components.json is only worth reading if this repository
// proves it, and proves it by RUNNING rather than by describing.
//
// estate-gates cannot do this. It has no Go toolchain, and building twenty-two
// repositories in its CI is a matrix it does not have. This repository already
// runs its suite on every push, so the marginal cost of a process start is
// seconds.
//
// What is proved here is exactly the `checked` bucket and nothing else. The
// `declared` bucket is not asserted against anything, on purpose: a test that
// pretended to verify a sentence about purpose would be the failure this whole
// design exists to avoid.
package manifest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

type envVar struct {
	Required bool   `json:"required"`
	Default  string `json:"default"`
}

type component struct {
	Name    string `json:"name"`
	Class   string `json:"class"`
	Checked struct {
		Package                    string            `json:"package"`
		Env                        map[string]envVar `json:"env"`
		StartsWithEmptyEnvironment bool              `json:"starts_with_empty_environment"`
		ListenDefault              string            `json:"listen_default"`
		HealthPath                 string            `json:"health_path"`
	} `json:"checked"`
}

type manifest struct {
	Schema     string      `json:"schema"`
	Repo       string      `json:"repo"`
	Components []component `json:"components"`
}

func root(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func load(t *testing.T) (manifest, string) {
	t.Helper()
	r := root(t)
	b, err := os.ReadFile(filepath.Join(r, "components.json"))
	if err != nil {
		t.Fatalf("reading components.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parsing components.json: %v", err)
	}
	if len(m.Components) == 0 {
		t.Fatal("components.json declares no component, so every test here measured nothing")
	}
	return m, r
}

// THE ONE THAT CLOSES THE HOLE. A binary this repository builds and does not
// declare is invisible from outside by construction, which is what estate-gates
// invariant 18 says about its own `runs` field.
func TestEveryBinaryThisRepositoryBuildsIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)

	list := exec.Command("go", "list", "-f", "{{if eq .Name \"main\"}}{{.ImportPath}}{{end}}", "./...")
	// Without this the command runs in THIS package's directory and `./...`
	// means this package alone. It then finds no main package, and the test
	// passes while measuring nothing.
	list.Dir = r
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	built := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			built[line] = true
		}
	}
	if len(built) == 0 {
		t.Fatal("go list found no main package in this repository, so this measured nothing")
	}

	declared := map[string]bool{}
	for _, c := range m.Components {
		if c.Checked.Package == "" {
			t.Errorf("component %q declares no package", c.Name)
			continue
		}
		declared[c.Checked.Package] = true
	}

	for p := range built {
		if !declared[p] {
			t.Errorf("this repository builds %s and components.json does not declare it.\n"+
				"A component nobody declares is one no deployment can be asked to install.", p)
		}
	}
	for p := range declared {
		if !built[p] {
			t.Errorf("components.json declares %s and this repository does not build it", p)
		}
	}
}

// Every HERALDYX_ name in non-test source, against every one declared.
//
// It reads STRING LITERALS rather than walking calls to os.Getenv, and that is
// not laziness: config.go reads several of these through a local `getenv(name,
// fallback)` helper, so a reader that followed os.Getenv call sites would miss
// them and report a set that is quietly short.
func TestEveryEnvironmentVariableThisRepositoryReadsIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)

	name := regexp.MustCompile(`HERALDYX_[A-Z0-9_]+`)
	inSource := map[string]bool{}
	err := filepath.Walk(r, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, n := range name.FindAllString(string(b), -1) {
			inSource[n] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(inSource) == 0 {
		t.Fatal("no HERALDYX_ name found in any non-test .go file, so this measured nothing")
	}

	declared := map[string]bool{}
	for _, c := range m.Components {
		for k := range c.Checked.Env {
			declared[k] = true
		}
	}

	var missing, extra []string
	for n := range inSource {
		if !declared[n] {
			missing = append(missing, n)
		}
	}
	for n := range declared {
		if !inSource[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, n := range missing {
		t.Errorf("the code reads %s and components.json does not declare it", n)
	}
	for _, n := range extra {
		t.Errorf("components.json declares %s and no non-test source reads it", n)
	}
}

// A declared default is the string config.go actually falls back to.
func TestEveryDeclaredDefaultIsTheOneTheCodeFallsBackTo(t *testing.T) {
	m, r := load(t)

	b, err := os.ReadFile(filepath.Join(r, "internal", "config", "config.go"))
	if err != nil {
		t.Fatalf("reading config.go: %v", err)
	}
	pair := regexp.MustCompile(`"(HERALDYX_[A-Z0-9_]+)",\s*"([^"]*)"`)
	inCode := map[string]string{}
	for _, m := range pair.FindAllStringSubmatch(string(b), -1) {
		inCode[m[1]] = m[2]
	}
	if len(inCode) == 0 {
		t.Fatal("config.go no longer pairs a HERALDYX_ name with a fallback, so this measured nothing")
	}

	checked := 0
	for _, c := range m.Components {
		for k, v := range c.Checked.Env {
			if v.Default == "" {
				continue
			}
			checked++
			if got, ok := inCode[k]; !ok {
				t.Errorf("components.json gives %s the default %q and config.go pairs it with nothing", k, v.Default)
			} else if got != v.Default {
				t.Errorf("components.json says %s defaults to %q; config.go says %q", k, v.Default, got)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no declared default to check, so this measured nothing")
	}
}

// AND THE HALF NO CENTRAL FILE COULD EVER DO: start it.
//
// This is the OPPOSITE claim from vouchryx's, which must refuse without each of
// six variables. heraldyx has no required variable: every one has a default or
// is genuinely optional, and what matters is that it comes up with an EMPTY
// environment and stays up. stack-single says why in its own comment: a box
// that never configured mail must still come up.
//
// It is given a HERALDYX_EVENTS and HERALDYX_STATE under t.TempDir() rather
// than a literally empty environment, because the declared defaults point at
// /var/lib/stack, which CI does not have and must not create. The claim being
// proved is "no variable is REQUIRED", so nothing here is a required variable:
// both of these have defaults and are being redirected, not supplied.
func TestItStartsWithNothingConfiguredAndStaysUp(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a process")
	}
	m, r := load(t)

	var daemon component
	for _, c := range m.Components {
		if c.Class == "daemon" {
			daemon = c
			break
		}
	}
	if daemon.Class != "daemon" {
		t.Skip("this repository declares no daemon")
	}
	if !daemon.Checked.StartsWithEmptyEnvironment {
		t.Skip("the manifest does not claim it starts unconfigured")
	}
	for k, v := range daemon.Checked.Env {
		if v.Required {
			t.Fatalf("components.json marks %s required AND claims the daemon starts "+
				"with nothing configured. Those cannot both be true.", k)
		}
	}

	bin := filepath.Join(t.TempDir(), "heraldyx")
	build := exec.Command("go", "build", "-o", bin, daemon.Checked.Package)
	build.Dir = r
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the declared package: %v\n%s", err, out)
	}

	dir := t.TempDir()
	cmd := exec.Command(bin, "--from-now=false")
	cmd.Env = []string{
		"HERALDYX_EVENTS=" + filepath.Join(dir, "events"),
		"HERALDYX_STATE=" + filepath.Join(dir, "state.json"),
	}
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o755); err != nil {
		t.Fatalf("preparing the event directory: %v", err)
	}
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting it: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Long enough for a configuration refusal to have happened: it reads the
	// environment, opens its state file and prints its watch line before its
	// first poll. A process that was going to reject its configuration has
	// done so well inside this.
	select {
	case err := <-done:
		t.Fatalf("it exited instead of staying up: %v\nits output was:\n%s", err, out.String())
	case <-time.After(3 * time.Second):
	}

	// Staying up is the claim, and a process that stayed up saying nothing
	// would satisfy it while doing nothing at all.
	if !strings.Contains(out.String(), "watching") {
		t.Errorf("it stayed up but never said what it was watching. Its output was:\n%s", out.String())
	}
}
