// Tier 1 contract tests for contracts/SPEC.md (architecture.md 11.1).
//
// Test names are TestCrucible_INV_<NAME>_<case>. Every assertion goes through
// check or checkEqual, which count cases and failures per invariant; TestMain
// writes the counts to CRUCIBLE_RESULTS. No case reaches a path that runs an
// external program: every mcp.json has an empty buildCmd, and no fixture has
// a Node, Python or Go marker without an mcp.json beside it.
package harness_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
)

const (
	crucibleSeed = 20261006
	randomCases  = 500

	codeAccept           = 0
	errProjectUndetected = -1 // SPEC.md ERR_PROJECT_UNDETECTED
	errManifestInvalid   = -2 // SPEC.md ERR_MANIFEST_INVALID
	errServerNotFound    = -3 // SPEC.md ERR_SERVER_NOT_FOUND
	errUnexpected        = -99
)

// Every invariant name from contracts/SPEC.md.
var invariants = []string{"INV_PROJECT_DETECTED", "INV_MANIFEST_JSON", "INV_SERVER_INSTALLED"}

// projectMarkers are the root files DetectAndBuild recognizes (README "How It Works").
var projectMarkers = []string{"mcp.json", "package.json", "pyproject.toml", "requirements.txt", "go.mod"}

// decoys look like markers but are not; none differs from a marker by case only.
var decoys = []string{
	"mcp.jsonx", ".mcp.json", "mcp.json.bak", "mcp", "package.json.orig", "package.jsonc",
	"packagejson", "pyproject.tml", "pyproject.toml~", "requirements.in", "requirements-dev.txt",
	"go.sum", "go.mod.orig", "gomod", "README.md", "Makefile", "main.go", "index.js",
}

type tally struct {
	Cases    int `json:"cases"`
	Failures int `json:"failures"`
}

var (
	mu      sync.Mutex
	results = map[string]*tally{}
)

// record counts one case for the invariant and one failure if ok is false.
func record(invariant string, ok bool) {
	mu.Lock()
	defer mu.Unlock()
	if results[invariant] == nil {
		results[invariant] = &tally{}
	}
	results[invariant].Cases++
	if !ok {
		results[invariant].Failures++
	}
}

// check compares two outcome codes.
func check(t *testing.T, invariant string, got, want int, label string) {
	t.Helper()
	record(invariant, got == want)
	if got != want {
		t.Errorf("%s %s: got code %d want %d", invariant, label, got, want)
	}
}

// checkEqual compares two values of an accepted result.
func checkEqual(t *testing.T, invariant string, got, want any, label string) {
	t.Helper()
	ok := reflect.DeepEqual(got, want)
	record(invariant, ok)
	if !ok {
		t.Errorf("%s %s: got %#v want %#v", invariant, label, got, want)
	}
}

// buildExtensions are the file types `go test` compiles into this package.
var buildExtensions = map[string]bool{
	".go": true, ".s": true, ".S": true, ".c": true, ".cc": true, ".cpp": true, ".cxx": true,
	".h": true, ".hh": true, ".hpp": true, ".hxx": true, ".m": true, ".f": true, ".F": true,
	".for": true, ".f90": true, ".syso": true, ".swig": true, ".swigcxx": true,
}

// foreignBuildFiles lists files in the package directory (the test's working
// directory) that go test would compile besides this file. A git-ignored one
// escapes the pins, and its init could fake the counts or skip the tests.
func foreignBuildFiles() ([]string, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var foreign []string
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != "crucible_contracts_test.go" && buildExtensions[filepath.Ext(entry.Name())] {
			foreign = append(foreign, entry.Name())
		}
	}
	return foreign, nil
}

// TestMain runs the tests, then writes the result file (also when tests fail).
// It drops any counts recorded before m.Run and runs nothing next to a foreign
// build file, so every reported case comes from a test in this file.
func TestMain(m *testing.M) {
	results = map[string]*tally{}
	code := 1
	foreign, err := foreignBuildFiles()
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "cannot list the harness package:", err)
	case len(foreign) > 0:
		fmt.Fprintln(os.Stderr, "foreign build files in the harness package:", foreign)
	default:
		code = m.Run()
	}
	table := map[string]*tally{}
	for _, name := range invariants {
		table[name] = &tally{}
		if recorded := results[name]; recorded != nil {
			table[name] = recorded
		}
	}
	document := map[string]any{"nonce": os.Getenv("CRUCIBLE_RESULTS_NONCE"), "invariants": table}
	data, err := json.Marshal(document)
	if err == nil {
		err = os.WriteFile(os.Getenv("CRUCIBLE_RESULTS"), data, 0o600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot write CRUCIBLE_RESULTS:", err)
		code = 1
	}
	os.Exit(code)
}

// errorCode maps an error from the project to its SPEC.md code. The project
// returns fmt.Errorf errors, so the map uses the message (SPEC.md ## Gaps).
func errorCode(err error) int {
	if err == nil {
		return codeAccept
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "could not detect project type"):
		return errProjectUndetected
	case strings.HasPrefix(message, "invalid mcp.json"):
		return errManifestInvalid
	case strings.HasPrefix(message, "server '") && strings.HasSuffix(message, "' not found in .mcp/servers/"):
		return errServerNotFound
	}
	return errUnexpected
}

// makeTree creates a temp directory with the given files (relative path -> content).
func makeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// detect calls DetectAndBuild on a tree and returns the code and the result.
func detect(t *testing.T, files map[string]string) (int, *builder.BuildResult) {
	t.Helper()
	result, err := builder.DetectAndBuild(makeTree(t, files))
	return errorCode(err), result
}

// inDir runs fn with the working directory set to dir, then restores it.
func inDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./ é→"

// token returns a random non-empty string from tokenAlphabet.
func token(rng *rand.Rand, maxLen int) string {
	runes := []rune(tokenAlphabet)
	length := 1 + rng.Intn(maxLen)
	out := make([]rune, length)
	for i := range out {
		out[i] = runes[rng.Intn(len(runes))]
	}
	return string(out)
}

// serverName returns a lowercase name that is a single path component.
func serverName(rng *rand.Rand) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const tail = "abcdefghijklmnopqrstuvwxyz0123456789-"
	length := 3 + rng.Intn(10)
	out := []byte{letters[rng.Intn(len(letters))]}
	for len(out) < length {
		out = append(out, tail[rng.Intn(len(tail))])
	}
	return string(out)
}

// randomManifest returns a Manifest with an empty BuildCmd, so nothing runs.
func randomManifest(rng *rand.Rand) builder.Manifest {
	types := []string{"", "node", "python", "go"}
	args := make([]string, rng.Intn(5))
	for i := range args {
		args[i] = token(rng, 24)
	}
	env := make([]string, rng.Intn(4))
	for i := range env {
		env[i] = strings.ToUpper(serverName(rng))
	}
	return builder.Manifest{Type: types[rng.Intn(len(types))], RunCmd: token(rng, 16), Args: args, RequiredEnv: env}
}

// checkManifestResult checks that an accepted manifest returns its fields unchanged.
func checkManifestResult(t *testing.T, invariant string, result *builder.BuildResult, want builder.Manifest, label string) {
	t.Helper()
	if result == nil {
		record(invariant, false)
		t.Errorf("%s %s: accepted with a nil result", invariant, label)
		return
	}
	checkEqual(t, invariant, result.Command, want.RunCmd, label+" runCmd")
	checkEqual(t, invariant, orEmpty(result.Args), orEmpty(want.Args), label+" args")
	checkEqual(t, invariant, orEmpty(result.EnvNeeds), orEmpty(want.RequiredEnv), label+" requiredEnv")
}

// orEmpty treats a nil slice and an empty slice as the same value.
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// INV_PROJECT_DETECTED: no marker in the root -> REJECT(ERR_PROJECT_UNDETECTED)
func TestCrucible_INV_PROJECT_DETECTED_boundary(t *testing.T) {
	code, result := detect(t, map[string]string{"mcp.json": `{"runCmd":"run"}`})
	check(t, "INV_PROJECT_DETECTED", code, codeAccept, "only mcp.json")
	if code == codeAccept {
		checkManifestResult(t, "INV_PROJECT_DETECTED", result, builder.Manifest{RunCmd: "run"}, "only mcp.json")
	}
	code, _ = detect(t, map[string]string{})
	check(t, "INV_PROJECT_DETECTED", code, errProjectUndetected, "empty directory")
	for _, marker := range projectMarkers {
		code, _ = detect(t, map[string]string{filepath.Join("src", marker): "{}"})
		check(t, "INV_PROJECT_DETECTED", code, errProjectUndetected, "marker only in a subdirectory: "+marker)
	}
	for _, decoy := range decoys {
		code, _ = detect(t, map[string]string{decoy: "{}"})
		check(t, "INV_PROJECT_DETECTED", code, errProjectUndetected, "decoy "+decoy)
	}
}

func TestCrucible_INV_PROJECT_DETECTED_random(t *testing.T) {
	rng := rand.New(rand.NewSource(crucibleSeed))
	for i := 0; i < randomCases; i++ {
		files := map[string]string{}
		for _, decoy := range decoys {
			if rng.Intn(3) == 0 {
				files[decoy] = token(rng, 32)
			}
		}
		label := fmt.Sprintf("case=%d", i)
		if rng.Intn(2) == 0 {
			// No marker in the root: rejected.
			code, _ := detect(t, files)
			check(t, "INV_PROJECT_DETECTED", code, errProjectUndetected, label+" decoys only")
			continue
		}
		// mcp.json plus any other markers: the manifest wins, nothing is built.
		manifest := randomManifest(rng)
		data, _ := json.Marshal(manifest)
		files["mcp.json"] = string(data)
		for _, marker := range projectMarkers[1:] {
			if rng.Intn(2) == 0 {
				files[marker] = "{}"
			}
		}
		code, result := detect(t, files)
		check(t, "INV_PROJECT_DETECTED", code, codeAccept, label+" with mcp.json")
		if code == codeAccept {
			checkManifestResult(t, "INV_PROJECT_DETECTED", result, manifest, label)
		}
	}
}

// INV_MANIFEST_JSON: mcp.json does not decode into Manifest -> REJECT(ERR_MANIFEST_INVALID)
func TestCrucible_INV_MANIFEST_JSON_boundary(t *testing.T) {
	accepted := map[string]builder.Manifest{
		`{}`:      {},
		`null`:    {},
		`{"x":1}`: {},
		`{"runCmd":"r","args":[],"requiredEnv":[]}`: {RunCmd: "r", Args: []string{}, RequiredEnv: []string{}},
		// runCmd is taken literally (builder.go buildFromManifest), also when it names a program on PATH.
		`{"runCmd":"go"}`:                                     {RunCmd: "go"},
		`{"type":"python","runCmd":"python"}`:                 {RunCmd: "python"},
		`{"type":"node","runCmd":"node","args":["index.js"]}`: {RunCmd: "node", Args: []string{"index.js"}},
	}
	for content, want := range accepted {
		code, result := detect(t, map[string]string{"mcp.json": content})
		check(t, "INV_MANIFEST_JSON", code, codeAccept, "valid "+content)
		if code == codeAccept {
			checkManifestResult(t, "INV_MANIFEST_JSON", result, want, "valid "+content)
		}
	}
	rejected := []string{``, `{`, `[]`, `"text"`, `1`, `{"runCmd":1}`, `{"args":"x"}`, `{"requiredEnv":[1]}`, `{"buildCmd":true}`, `{}x`}
	for _, content := range rejected {
		code, _ := detect(t, map[string]string{"mcp.json": content})
		check(t, "INV_MANIFEST_JSON", code, errManifestInvalid, "invalid "+content)
	}
}

// wrongTypes gives each Manifest field a JSON value of the wrong type.
var wrongTypes = map[string]any{"type": 7, "buildCmd": false, "runCmd": []int{1}, "args": "a", "requiredEnv": map[string]int{"k": 1}}

func TestCrucible_INV_MANIFEST_JSON_random(t *testing.T) {
	rng := rand.New(rand.NewSource(crucibleSeed + 1))
	fields := []string{"type", "buildCmd", "runCmd", "args", "requiredEnv"}
	for i := 0; i < randomCases; i++ {
		label := fmt.Sprintf("case=%d", i)
		manifest := randomManifest(rng)
		data, _ := json.Marshal(manifest)

		code, result := detect(t, map[string]string{"mcp.json": string(data)})
		check(t, "INV_MANIFEST_JSON", code, codeAccept, label+" valid")
		if code == codeAccept {
			checkManifestResult(t, "INV_MANIFEST_JSON", result, manifest, label)
		}

		// A proper prefix of a JSON object is never valid JSON.
		cut := rng.Intn(len(data))
		code, _ = detect(t, map[string]string{"mcp.json": string(data[:cut])})
		check(t, "INV_MANIFEST_JSON", code, errManifestInvalid, fmt.Sprintf("%s truncated at %d", label, cut))

		field := fields[rng.Intn(len(fields))]
		broken := map[string]any{"runCmd": manifest.RunCmd, "args": manifest.Args, field: wrongTypes[field]}
		data, _ = json.Marshal(broken)
		code, _ = detect(t, map[string]string{"mcp.json": string(data)})
		check(t, "INV_MANIFEST_JSON", code, errManifestInvalid, label+" wrong type for "+field)
	}
}

// installServers creates .mcp/servers/<name> for every name under root.
func installServers(t *testing.T, root string, names []string) {
	t.Helper()
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(root, ".mcp", "servers", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// serverCode calls GetServerPath and checks the path of an accepted name.
func serverCode(t *testing.T, name, label string) int {
	t.Helper()
	path, err := fetcher.GetServerPath(name)
	code := errorCode(err)
	if code == codeAccept {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			t.Fatal(cwdErr)
		}
		checkEqual(t, "INV_SERVER_INSTALLED", path, filepath.Join(cwd, ".mcp", "servers", name), label+" path")
	}
	return code
}

// checkList checks that ListServers returns exactly the installed directories.
func checkList(t *testing.T, want []string, label string) {
	t.Helper()
	got, err := fetcher.ListServers()
	check(t, "INV_SERVER_INSTALLED", errorCode(err), codeAccept, label+" ListServers error")
	got = append([]string{}, got...)
	want = append([]string{}, want...)
	sort.Strings(got)
	sort.Strings(want)
	checkEqual(t, "INV_SERVER_INSTALLED", got, want, label+" ListServers")
}

// addFile creates a regular file under .mcp/servers; it is not a server.
func addFile(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".mcp", "servers", name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// INV_SERVER_INSTALLED: no entry under .mcp/servers -> REJECT(ERR_SERVER_NOT_FOUND)
func TestCrucible_INV_SERVER_INSTALLED_boundary(t *testing.T) {
	root := t.TempDir()
	inDir(t, root, func() {
		check(t, "INV_SERVER_INSTALLED", serverCode(t, "alpha", "no .mcp"), errServerNotFound, "no .mcp directory")
		checkList(t, nil, "no .mcp directory")
		if err := os.MkdirAll(filepath.Join(root, ".mcp", "servers"), 0o755); err != nil {
			t.Fatal(err)
		}
		check(t, "INV_SERVER_INSTALLED", serverCode(t, "alpha", "empty"), errServerNotFound, "empty servers directory")
		installServers(t, root, []string{"alpha"})
		check(t, "INV_SERVER_INSTALLED", serverCode(t, "alpha", "installed"), codeAccept, "installed")
		addFile(t, root, "notes.txt")
		checkList(t, []string{"alpha"}, "one server and one file")
		check(t, "INV_SERVER_INSTALLED", serverCode(t, "alphb", "neighbour"), errServerNotFound, "neighbour name")
		check(t, "INV_SERVER_INSTALLED", serverCode(t, "alph", "prefix"), errServerNotFound, "prefix of an installed name")
		check(t, "INV_SERVER_INSTALLED", serverCode(t, "alphaa", "extension"), errServerNotFound, "installed name plus a letter")
	})
}

func TestCrucible_INV_SERVER_INSTALLED_random(t *testing.T) {
	rng := rand.New(rand.NewSource(crucibleSeed + 2))
	root := t.TempDir()
	installed := map[string]bool{}
	var names []string
	for len(names) < 40 {
		name := serverName(rng)
		if !installed[name] {
			installed[name] = true
			names = append(names, name)
		}
	}
	installServers(t, root, names)
	for i := 0; i < 5; i++ {
		addFile(t, root, fmt.Sprintf("file-%d.txt", rng.Intn(1000)))
	}
	inDir(t, root, func() {
		checkList(t, names, "40 servers and files")
		for i := 0; i < randomCases; i++ {
			label := fmt.Sprintf("case=%d", i)
			if rng.Intn(2) == 0 {
				name := names[rng.Intn(len(names))]
				check(t, "INV_SERVER_INSTALLED", serverCode(t, name, label), codeAccept, label+" installed "+name)
				continue
			}
			name := serverName(rng)
			for installed[name] {
				name = serverName(rng)
			}
			check(t, "INV_SERVER_INSTALLED", serverCode(t, name, label), errServerNotFound, label+" missing "+name)
		}
	})
}
