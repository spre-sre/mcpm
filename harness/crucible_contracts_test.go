// Tier 1 contract tests for contracts/SPEC.md (architecture.md 11.1).
//
// Test names are TestCrucible_INV_<NAME>_<case>. Every assertion goes through
// check or checkEqual, which count cases and failures per invariant; TestMain
// writes the counts to CRUCIBLE_RESULTS. No case reaches a path that runs an
// external program on correct code: a valid mcp.json has an empty buildCmd,
// and no fixture has a Node, Python or Go marker without an mcp.json beside
// it. One case has a buildCmd in a manifest that must be rejected first.
package harness_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
	"mcpm/internal/server"
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
var invariants = []string{
	"INV_PROJECT_DETECTED", "INV_MANIFEST_JSON", "INV_SERVER_INSTALLED",
	"INV_HOST_PORT_SPLIT", "INV_HOST_WITHOUT_PORT",
}

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

// detectIn calls DetectAndBuild on a new tree and returns the code, the result and the tree.
func detectIn(t *testing.T, files map[string]string) (int, *builder.BuildResult, string) {
	t.Helper()
	dir := makeTree(t, files)
	result, err := builder.DetectAndBuild(dir)
	return errorCode(err), result, dir
}

// detect calls DetectAndBuild on a new tree and returns the code and the result.
func detect(t *testing.T, files map[string]string) (int, *builder.BuildResult) {
	t.Helper()
	code, result, _ := detectIn(t, files)
	return code, result
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

// manifestCase is a valid manifest, the files its args name, and which
// values the result must make absolute against the server directory.
type manifestCase struct {
	manifest builder.Manifest
	files    map[string]string // created beside mcp.json
	absCmd   bool              // runCmd is a relative path
	absArgs  []bool            // args[i] names a created file or directory
}

// want returns the result the case must give when built in dir.
func (c manifestCase) want(dir string) builder.Manifest {
	want := c.manifest
	if c.absCmd {
		want.RunCmd = filepath.Join(dir, want.RunCmd)
	}
	want.Args = append([]string(nil), c.manifest.Args...)
	for i, abs := range c.absArgs {
		if abs {
			want.Args[i] = filepath.Join(dir, want.Args[i])
		}
	}
	return want
}

// randomRunCmd returns a runCmd and whether it is a relative path.
func randomRunCmd(rng *rand.Rand) (string, bool) {
	name := serverName(rng)
	switch rng.Intn(4) {
	case 0:
		return "bin/" + name, true
	case 1:
		return "./" + name, true
	case 2:
		return "/usr/local/bin/" + name, false
	}
	return name, false // a bare name stays a PATH lookup
}

// randomArg returns an arg, a file to create for it (or ""), and whether it must be resolved.
func randomArg(rng *rand.Rand) (string, string, bool) {
	name := serverName(rng)
	switch rng.Intn(6) {
	case 0:
		return "--" + name, "", false // a flag
	case 1:
		return "/opt/" + name + "/index.js", "", false // already absolute
	case 2:
		return "dist/" + name + ".js", "dist/" + name + ".js", true // a built file
	case 3:
		return name + ".py", name + ".py", true // a file in the root
	case 4:
		return "missing/" + name + ".js", "", false // a relative path that does not exist
	}
	return "@scope/" + name, "", false // a package name, not a path
}

// randomManifestCase returns a valid manifest with an empty BuildCmd, so nothing runs.
func randomManifestCase(rng *rand.Rand) manifestCase {
	types := []string{"", "node", "python", "go"}
	c := manifestCase{files: map[string]string{}}
	c.manifest.Type = types[rng.Intn(len(types))]
	c.manifest.RunCmd, c.absCmd = randomRunCmd(rng)
	c.manifest.Args = make([]string, rng.Intn(5))
	c.absArgs = make([]bool, len(c.manifest.Args))
	for i := range c.manifest.Args {
		var file string
		c.manifest.Args[i], file, c.absArgs[i] = randomArg(rng)
		if file != "" {
			c.files[file] = "x"
		}
	}
	c.manifest.RequiredEnv = make([]string, rng.Intn(4))
	for i := range c.manifest.RequiredEnv {
		c.manifest.RequiredEnv[i] = strings.ToUpper(serverName(rng))
	}
	return c
}

// checkManifestResult checks an accepted manifest's result against want.
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
		c := randomManifestCase(rng)
		data, _ := json.Marshal(c.manifest)
		files["mcp.json"] = string(data)
		for name, content := range c.files {
			files[name] = content
		}
		for _, marker := range projectMarkers[1:] {
			if rng.Intn(2) == 0 {
				files[marker] = "{}"
			}
		}
		code, result, dir := detectIn(t, files)
		check(t, "INV_PROJECT_DETECTED", code, codeAccept, label+" with mcp.json")
		if code == codeAccept {
			checkManifestResult(t, "INV_PROJECT_DETECTED", result, c.want(dir), label)
		}
	}
}

// INV_MANIFEST_JSON: mcp.json does not decode into Manifest, or has no runCmd -> REJECT(ERR_MANIFEST_INVALID)
func TestCrucible_INV_MANIFEST_JSON_boundary(t *testing.T) {
	same := func(m builder.Manifest) func(string) builder.Manifest {
		return func(string) builder.Manifest { return m }
	}
	accepted := []struct {
		content string
		files   map[string]string
		want    func(dir string) builder.Manifest
	}{
		{`{"runCmd":"r","args":[],"requiredEnv":[]}`, nil, same(builder.Manifest{RunCmd: "r"})},
		// A bare runCmd stays a PATH lookup, also when it names a program on PATH.
		{`{"runCmd":"go"}`, nil, same(builder.Manifest{RunCmd: "go"})},
		{`{"type":"python","runCmd":"python"}`, nil, same(builder.Manifest{RunCmd: "python"})},
		// The README example: a relative arg that names a file becomes absolute.
		{`{"type":"node","buildCmd":"","runCmd":"node","args":["dist/index.js"],"requiredEnv":["API_KEY"]}`,
			map[string]string{"dist/index.js": "x"},
			func(dir string) builder.Manifest {
				return builder.Manifest{RunCmd: "node", Args: []string{filepath.Join(dir, "dist", "index.js")}, RequiredEnv: []string{"API_KEY"}}
			}},
		{`{"runCmd":"node","args":["index.js"]}`, map[string]string{"index.js": "x"},
			func(dir string) builder.Manifest {
				return builder.Manifest{RunCmd: "node", Args: []string{filepath.Join(dir, "index.js")}}
			}},
		{`{"runCmd":"node","args":["lib"]}`, map[string]string{"lib/x.js": "x"},
			func(dir string) builder.Manifest {
				return builder.Manifest{RunCmd: "node", Args: []string{filepath.Join(dir, "lib")}}
			}},
		// A flag stays a flag, also when a file of that name exists.
		{`{"runCmd":"node","args":["-x","--port","index.js"]}`, map[string]string{"-x": "x", "--port": "x", "index.js": "x"},
			func(dir string) builder.Manifest {
				return builder.Manifest{RunCmd: "node", Args: []string{"-x", "--port", filepath.Join(dir, "index.js")}}
			}},
		// A relative arg that names nothing in the directory stays as written.
		{`{"runCmd":"node","args":["index.js"]}`, nil, same(builder.Manifest{RunCmd: "node", Args: []string{"index.js"}})},
		// A relative runCmd path becomes absolute; flags, values, package names and "" stay.
		{`{"runCmd":"./server"}`, nil, func(dir string) builder.Manifest { return builder.Manifest{RunCmd: filepath.Join(dir, "server")} }},
		{`{"runCmd":"bin/server","args":["--port","8080","@scope/pkg",""]}`, nil,
			func(dir string) builder.Manifest {
				return builder.Manifest{RunCmd: filepath.Join(dir, "bin", "server"), Args: []string{"--port", "8080", "@scope/pkg", ""}}
			}},
		{`{"runCmd":"/usr/bin/env","args":["/abs/x.js"]}`, nil, same(builder.Manifest{RunCmd: "/usr/bin/env", Args: []string{"/abs/x.js"}})},
	}
	for _, c := range accepted {
		files := map[string]string{"mcp.json": c.content}
		for name, content := range c.files {
			files[name] = content
		}
		code, result, dir := detectIn(t, files)
		check(t, "INV_MANIFEST_JSON", code, codeAccept, "valid "+c.content)
		if code == codeAccept {
			checkManifestResult(t, "INV_MANIFEST_JSON", result, c.want(dir), "valid "+c.content)
		}
	}
	rejected := []string{
		``, `{`, `[]`, `"text"`, `1`, `{"runCmd":1}`, `{"args":"x"}`, `{"requiredEnv":[1]}`, `{"buildCmd":true}`, `{}x`,
		// No runCmd.
		`{}`, `null`, `{"x":1}`, `{"runCmd":""}`, `{"runCmd":"   "}`, `{"runCmd":"\t\n"}`, `{"args":["index.js"]}`,
	}
	for _, content := range rejected {
		code, _ := detect(t, map[string]string{"mcp.json": content})
		check(t, "INV_MANIFEST_JSON", code, errManifestInvalid, "invalid "+content)
	}
	// The manifest is rejected before its buildCmd runs.
	code, _, dir := detectIn(t, map[string]string{"mcp.json": `{"buildCmd":"touch buildcmd-ran"}`})
	check(t, "INV_MANIFEST_JSON", code, errManifestInvalid, "no runCmd with a buildCmd")
	_, err := os.Stat(filepath.Join(dir, "buildcmd-ran"))
	check(t, "INV_MANIFEST_JSON", errorCode(err), errUnexpected, "buildCmd did not run")
}

// wrongTypes gives each Manifest field a JSON value of the wrong type.
var wrongTypes = map[string]any{"type": 7, "buildCmd": false, "runCmd": []int{1}, "args": "a", "requiredEnv": map[string]int{"k": 1}}

func TestCrucible_INV_MANIFEST_JSON_random(t *testing.T) {
	rng := rand.New(rand.NewSource(crucibleSeed + 1))
	fields := []string{"type", "buildCmd", "runCmd", "args", "requiredEnv"}
	for i := 0; i < randomCases; i++ {
		label := fmt.Sprintf("case=%d", i)
		c := randomManifestCase(rng)
		manifest := c.manifest
		data, _ := json.Marshal(manifest)

		files := map[string]string{"mcp.json": string(data)}
		for name, content := range c.files {
			files[name] = content
		}
		code, result, dir := detectIn(t, files)
		check(t, "INV_MANIFEST_JSON", code, codeAccept, label+" valid")
		if code == codeAccept {
			checkManifestResult(t, "INV_MANIFEST_JSON", result, c.want(dir), label)
		}

		// The same manifest without a runCmd: empty or whitespace only.
		blank := manifest
		blank.RunCmd = []string{"", " ", "\t", " \n "}[rng.Intn(4)]
		data, _ = json.Marshal(blank)
		code, _ = detect(t, map[string]string{"mcp.json": string(data)})
		check(t, "INV_MANIFEST_JSON", code, errManifestInvalid, fmt.Sprintf("%s runCmd=%q", label, blank.RunCmd))
		data, _ = json.Marshal(manifest)

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

// randomHostName returns a DNS-like name: letters, digits, '-' and '.', no colon.
func randomHostName(rng *rand.Rand) string {
	labels := make([]string, 1+rng.Intn(3))
	for i := range labels {
		labels[i] = serverName(rng)
	}
	return strings.Join(labels, ".")
}

// randomIPv4 returns a dotted IPv4 address.
func randomIPv4(rng *rand.Rand) string {
	return fmt.Sprintf("%d.%d.%d.%d", rng.Intn(256), rng.Intn(256), rng.Intn(256), rng.Intn(256))
}

// randomIPv6 returns an IPv6 address with at least two colons: full, compressed
// with "::", or with a zone.
func randomIPv6(rng *rand.Rand) string {
	groups := make([]string, 8)
	for i := range groups {
		groups[i] = fmt.Sprintf("%x", rng.Intn(1<<16))
	}
	address := strings.Join(groups, ":")
	if rng.Intn(2) == 0 {
		cut := rng.Intn(7)
		address = strings.Join(groups[:cut], ":") + "::" + strings.Join(groups[cut+2:], ":")
	}
	if rng.Intn(4) == 0 {
		address += "%eth" + fmt.Sprint(rng.Intn(4))
	}
	return address
}

// randomHost returns a host of a random shape, without brackets or a port.
func randomHost(rng *rand.Rand) string {
	switch rng.Intn(3) {
	case 0:
		return randomHostName(rng)
	case 1:
		return randomIPv4(rng)
	}
	return randomIPv6(rng)
}

// randomPort returns a decimal port, or a value net.SplitHostPort also accepts.
func randomPort(rng *rand.Rand) string {
	switch rng.Intn(6) {
	case 0:
		return ""
	case 1:
		return "http"
	}
	return fmt.Sprint(rng.Intn(1 << 16))
}

// checkSplit checks StripHostPort(raw) against the host net.SplitHostPort returns.
func checkSplit(t *testing.T, raw, label string) {
	t.Helper()
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		record("INV_HOST_PORT_SPLIT", false)
		t.Errorf("INV_HOST_PORT_SPLIT %s: test input %q does not split: %v", label, raw, err)
		return
	}
	checkEqual(t, "INV_HOST_PORT_SPLIT", server.StripHostPort(raw), host, fmt.Sprintf("%s %q", label, raw))
}

// INV_HOST_PORT_SPLIT: net.SplitHostPort(x) = (h, p, nil) ∧ StripHostPort(x) ≠ h -> REJECT(ERR_HOST_PORT_SPLIT)
func TestCrucible_INV_HOST_PORT_SPLIT_boundary(t *testing.T) {
	inputs := []string{
		"localhost:8080", "[::1]:8080", "127.0.0.1:9000", "example.com:http", ":80", ":", "host:",
		"[]:80", "[::1]:", "[fe80::1%eth0]:443", "[example.com]:80", "[a:b]:1",
	}
	for _, raw := range inputs {
		checkSplit(t, raw, "boundary")
	}
}

func TestCrucible_INV_HOST_PORT_SPLIT_random(t *testing.T) {
	rng := rand.New(rand.NewSource(crucibleSeed + 3))
	for i := 0; i < randomCases; i++ {
		checkSplit(t, net.JoinHostPort(randomHost(rng), randomPort(rng)), fmt.Sprintf("case=%d", i))
	}
}

// INV_HOST_WITHOUT_PORT: an unbracketed host whose colon count is not 1 stays
// unchanged; "[h]" with h ∌ ']' gives h -> REJECT(ERR_HOST_WITHOUT_PORT)
func TestCrucible_INV_HOST_WITHOUT_PORT_boundary(t *testing.T) {
	cases := map[string]string{
		"": "", "example.com": "example.com", "127.0.0.1": "127.0.0.1",
		"::1": "::1", "::": "::", "2001:db8::1": "2001:db8::1", "fe80::1%eth0": "fe80::1%eth0",
		"1:2:3:4:5:6:7:8": "1:2:3:4:5:6:7:8",
		"[::1]":           "::1", "[]": "", "[example.com]": "example.com", "[fe80::1%eth0]": "fe80::1%eth0",
	}
	for raw, want := range cases {
		checkEqual(t, "INV_HOST_WITHOUT_PORT", server.StripHostPort(raw), want, fmt.Sprintf("boundary %q", raw))
	}
}

func TestCrucible_INV_HOST_WITHOUT_PORT_random(t *testing.T) {
	rng := rand.New(rand.NewSource(crucibleSeed + 4))
	for i := 0; i < randomCases; i++ {
		host := randomHost(rng) // no brackets; 0 colons or at least 2
		raw, want := host, host
		if rng.Intn(2) == 0 {
			raw = "[" + host + "]"
		}
		checkEqual(t, "INV_HOST_WITHOUT_PORT", server.StripHostPort(raw), want, fmt.Sprintf("case=%d %q", i, raw))
	}
}
