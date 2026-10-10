// Command gate-ast is the crucible AST gate for Go: a McCabe complexity limit
// with a baseline ratchet. It uses the Go standard library only.
//
// Usage: go run ./cmd/gate-ast [-json] [-limit N] [-baseline PATH]
//
//	[-snapshot | -tighten | -migrate-baseline | -verify-migration]
//	[dir|file|dir/...]...
//
// Counting (base 1 per unit): +1 for each if, for, range, non-default case
// and non-default select case, and each && or ||. Every function declaration
// is a unit, and every function literal inside it counts toward it, so a
// closure (immediately invoked or not) cannot split one function's branches
// into units that each pass. A literal outside any function declaration
// (for example a cobra RunE in a var) is its own unit, glob.funcN, and the
// literals inside it count toward it. A repeated key in one file gets #2, #3
// (counting version 2).
//
// Baseline (.crucible/baseline.json): {"version": 2, "functions": {key: M}}.
// -migrate-baseline writes an optional "_provenance" object next to it;
// -tighten keeps that object.
//
// Exit codes: 0 pass, 1 violations, 2 error.
package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	baselineVersion = 2
	exitPass        = 0
	exitViolations  = 1
	exitError       = 2
)

type options struct {
	limit           int
	baseline        string
	snapshot        bool
	tighten         bool
	migrate         bool
	verifyMigration bool
	asJSON          bool
	paths           []string
}

type unit struct {
	key     string
	file    string
	name    string
	line    int
	score   int
	scoreV1 int // counting v1 (literals excluded); read only by -migrate-baseline, removed in kit v3.0
}

type problem struct {
	file string
	msg  string
}

type detail struct {
	Stage         string `json:"stage"`
	File          string `json:"file"`
	Function      string `json:"function"`
	Line          int    `json:"line"`
	Score         int    `json:"score"`
	Limit         int    `json:"limit"`
	Grandfathered bool   `json:"grandfathered"`
	Error         string `json:"error,omitempty"`
}

type change struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	From int    `json:"from"`
	To   *int   `json:"to"`
}

type report struct {
	OK               bool     `json:"ok"`
	Violations       []string `json:"violations"`
	Details          []detail `json:"violation_details"`
	FunctionsChecked int      `json:"functions_checked"`
	FilesChecked     int      `json:"files_checked"`
	Tightenable      []change `json:"tightenable"`
	*MigrationInfo
}

// MigrationInfo is present, with every key, only in the migration modes.
type MigrationInfo struct {
	Action          string         `json:"action"`
	Genesis         string         `json:"genesis"`
	EngineSHA256    string         `json:"engine_sha256"`
	Migration       []migrationRow `json:"migration_rows"`
	GenesisNotes    []string       `json:"genesis_notes"`
	Dirty           bool           `json:"work_tree_dirty"`
	BaselineWritten string         `json:"baseline_written,omitempty"`
}

// scope carries what the unit walk needs for one file.
type scope struct {
	fset    *token.FileSet
	rel     string
	globals int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		opts.asJSON = wantsJSON(args)
		emit(errorReport(err.Error()), opts.asJSON, stdout, stderr)
		return exitError
	}
	rep, code := execute(opts)
	emit(rep, opts.asJSON, stdout, stderr)
	return code
}

func wantsJSON(args []string) bool {
	for _, arg := range args {
		if arg == "-json" || arg == "--json" || arg == "-json=true" || arg == "--json=true" {
			return true
		}
	}
	return false
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	set := flag.NewFlagSet("gate-ast", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.IntVar(&opts.limit, "limit", 10, "maximum McCabe complexity per function")
	set.StringVar(&opts.baseline, "baseline", ".crucible/baseline.json", "baseline file")
	set.BoolVar(&opts.snapshot, "snapshot", false, "write the baseline (refused if it exists)")
	set.BoolVar(&opts.tighten, "tighten", false, "lower or remove baseline entries only")
	set.BoolVar(&opts.migrate, "migrate-baseline", false, "rewrite a version-1 baseline for counting v2 (genesis from the committed .crucible/pins.json)")
	set.BoolVar(&opts.verifyMigration, "verify-migration", false, "re-derive the migration and compare it with the staged baseline")
	set.BoolVar(&opts.asJSON, "json", false, "print one JSON object on stdout")
	if err := set.Parse(args); err != nil {
		return opts, err
	}
	if opts.limit < 1 {
		return opts, fmt.Errorf("-limit must be at least 1")
	}
	if modeCount(opts) > 1 {
		return opts, fmt.Errorf("-snapshot, -tighten, -migrate-baseline and -verify-migration cannot be combined")
	}
	opts.paths = set.Args()
	if len(opts.paths) == 0 {
		opts.paths = []string{"."}
	}
	return opts, nil
}

func modeCount(opts options) int {
	count := 0
	for _, on := range []bool{opts.snapshot, opts.tighten, opts.migrate, opts.verifyMigration} {
		if on {
			count++
		}
	}
	return count
}

func errorReport(msg string) report {
	return report{Violations: []string{msg}, Details: []detail{}, Tightenable: []change{}}
}

func execute(opts options) (report, int) {
	if opts.migrate || opts.verifyMigration {
		return migrateBaseline(opts)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return errorReport(err.Error()), exitError
	}
	files, problems := collectFiles(opts.paths)
	var units []unit
	if len(problems) == 0 {
		units, problems = analyzeFiles(files, cwd)
	}
	if len(problems) > 0 {
		return problemReport(problems, opts.limit, len(files)), exitError
	}
	base, err := loadBaseline(opts.baseline)
	if err != nil {
		return errorReport(err.Error()), exitError
	}
	if opts.snapshot {
		return snapshot(opts, base, units, len(files))
	}
	return judge(opts, base, units, scannedSet(files, cwd), len(files))
}

func problemReport(problems []problem, limit, files int) report {
	rep := errorReport("")
	rep.Violations = nil
	rep.FilesChecked = files
	for _, item := range problems {
		rep.Violations = append(rep.Violations, item.file+": "+item.msg)
		rep.Details = append(rep.Details, detail{Stage: "step0", File: item.file,
			Limit: limit, Error: item.msg})
	}
	return rep
}

func snapshot(opts options, base *baselineFile, units []unit, files int) (report, int) {
	if base.exists {
		return errorReport("baseline " + opts.baseline + " already exists; refusing to overwrite"), exitError
	}
	base.functions = map[string]int{}
	for _, item := range units {
		if item.score > opts.limit {
			base.functions[item.key] = item.score
		}
	}
	if err := base.save(opts.baseline, true); err != nil {
		return errorReport(err.Error()), exitError
	}
	rep := errorReport("")
	rep.Violations, rep.OK = []string{}, true
	rep.FunctionsChecked, rep.FilesChecked = len(units), files
	return rep, exitPass
}

func judge(opts options, base *baselineFile, units []unit, scanned map[string]bool, files int) (report, int) {
	rep := errorReport("")
	rep.Violations = []string{}
	rep.FunctionsChecked, rep.FilesChecked = len(units), files
	sort.Slice(units, func(i, j int) bool { return lessUnit(units[i], units[j]) })
	seen := map[string]bool{}
	for _, item := range units {
		seen[item.key] = true
		verdict(&rep, item, base.functions, opts.limit)
	}
	rep.Tightenable = append(rep.Tightenable, staleChanges(base.functions, seen, scanned)...)
	sort.Slice(rep.Tightenable, func(i, j int) bool { return rep.Tightenable[i].Key < rep.Tightenable[j].Key })
	if opts.tighten {
		if err := applyTighten(opts.baseline, base, rep.Tightenable); err != nil {
			return errorReport(err.Error()), exitError
		}
	}
	rep.OK = len(rep.Violations) == 0
	if !rep.OK {
		return rep, exitViolations
	}
	return rep, exitPass
}

func lessUnit(left, right unit) bool {
	if left.file != right.file {
		return left.file < right.file
	}
	if left.line != right.line {
		return left.line < right.line
	}
	return left.key < right.key
}

// verdict applies the limit and the ratchet to one unit.
func verdict(rep *report, item unit, base map[string]int, limit int) {
	recorded, grandfathered := base[item.key]
	effective, kind := limit, "limit"
	if grandfathered {
		effective, kind = recorded, "baseline"
	}
	if item.score > effective {
		rep.Violations = append(rep.Violations, fmt.Sprintf("%s:%d: %s has McCabe %d, over the %s %d",
			item.file, item.line, item.name, item.score, kind, effective))
		rep.Details = append(rep.Details, detail{Stage: "step0", File: item.file, Function: item.name,
			Line: item.line, Score: item.score, Limit: effective, Grandfathered: grandfathered})
		return
	}
	if grandfathered {
		rep.Tightenable = append(rep.Tightenable, lowered(item, recorded, limit)...)
	}
}

// lowered returns the baseline change a passing grandfathered unit allows.
func lowered(item unit, recorded, limit int) []change {
	if item.score <= limit {
		return []change{{Kind: "function", Key: item.key, From: recorded}}
	}
	if item.score < recorded {
		score := item.score
		return []change{{Kind: "function", Key: item.key, From: recorded, To: &score}}
	}
	return nil
}

// staleChanges lists baseline entries whose function is gone. An entry counts
// only when its file was scanned or no longer exists, so a narrow scan never
// removes entries of code it did not look at.
func staleChanges(base map[string]int, seen, scanned map[string]bool) []change {
	var out []change
	for key, recorded := range base {
		file, _, _ := strings.Cut(key, "::")
		if seen[key] || !(scanned[file] || !fileExists(file)) {
			continue
		}
		out = append(out, change{Kind: "function", Key: key, From: recorded})
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(filepath.FromSlash(path))
	return err == nil
}

func scannedSet(files []string, cwd string) map[string]bool {
	set := map[string]bool{}
	for _, path := range files {
		set[relPath(path, cwd)] = true
	}
	return set
}

func applyTighten(path string, base *baselineFile, changes []change) error {
	if len(changes) == 0 {
		return nil
	}
	for _, item := range changes {
		if item.To == nil {
			delete(base.functions, item.Key)
		} else {
			base.functions[item.Key] = *item.To
		}
	}
	return base.save(path, false)
}

// ---------------------------------------------------------------------------
// Baseline
// ---------------------------------------------------------------------------

// baselineFile keeps every top-level key, so other engines can share the file.
type baselineFile struct {
	raw       map[string]json.RawMessage
	functions map[string]int
	exists    bool
}

var provenanceKeys = []string{"engine_sha256", "genesis_commit", "timestamp", "version"}

func loadBaseline(path string) (*baselineFile, error) {
	return loadBaselineVersion(path, baselineVersion)
}

func loadBaselineVersion(path string, version int) (*baselineFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &baselineFile{raw: map[string]json.RawMessage{}, functions: map[string]int{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read baseline %s: %v", path, err)
	}
	return parseBaseline(data, path, version)
}

func parseBaseline(data []byte, label string, version int) (*baselineFile, error) {
	base := &baselineFile{raw: map[string]json.RawMessage{}, functions: map[string]int{}, exists: true}
	if err := json.Unmarshal(data, &base.raw); err != nil {
		return nil, fmt.Errorf("cannot read baseline %s: %v", label, err)
	}
	var found int
	if json.Unmarshal(base.raw["version"], &found) != nil || found != version {
		return nil, errors.New(versionMessage(label, found, version))
	}
	if err := checkProvenance(base.raw, label, version); err != nil {
		return nil, err
	}
	if raw, ok := base.raw["functions"]; ok {
		if err := json.Unmarshal(raw, &base.functions); err != nil || base.functions == nil {
			return nil, fmt.Errorf("baseline %s: 'functions' must map names to integers", label)
		}
	}
	return base, nil
}

func versionMessage(label string, found, wanted int) string {
	switch {
	case found == 1 && wanted == baselineVersion:
		return fmt.Sprintf("baseline %s uses counting version 1; run the baseline migration (see the archetype README)", label)
	case found == baselineVersion && wanted == 1:
		return fmt.Sprintf("baseline %s is already version %d: nothing to migrate", label, baselineVersion)
	}
	return fmt.Sprintf("baseline %s: unsupported or missing version", label)
}

func checkProvenance(raw map[string]json.RawMessage, label string, version int) error {
	value, ok := raw["_provenance"]
	if !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if version != baselineVersion || json.Unmarshal(value, &fields) != nil || !sameKeys(fields, provenanceKeys) {
		return fmt.Errorf("baseline %s: '_provenance' is malformed or appears in a version %d baseline", label, version)
	}
	return nil
}

func sameKeys(fields map[string]json.RawMessage, want []string) bool {
	if len(fields) != len(want) {
		return false
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func (base *baselineFile) save(path string, exclusive bool) error {
	functions, err := json.Marshal(base.functions)
	if err != nil {
		return err
	}
	base.raw["functions"] = functions
	base.raw["version"] = json.RawMessage(strconv.Itoa(baselineVersion))
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(base.raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if exclusive {
		return writeExclusive(path, buffer.Bytes())
	}
	return writeAtomic(path, buffer.Bytes())
}

func writeExclusive(path string, data []byte) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := handle.Write(data); err != nil {
		handle.Close()
		return err
	}
	return handle.Close()
}

func writeAtomic(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// ---------------------------------------------------------------------------
// File discovery
// ---------------------------------------------------------------------------

type collector struct {
	seen     map[string]bool
	problems []problem
}

func normalizeRoot(arg string) string {
	arg = strings.TrimSuffix(arg, "/...")
	if arg == "..." || arg == "" {
		return "."
	}
	return filepath.Clean(arg)
}

func collectFiles(roots []string) ([]string, []problem) {
	found := &collector{seen: map[string]bool{}}
	for _, arg := range roots {
		root := normalizeRoot(arg)
		info, err := os.Stat(root)
		switch {
		case err != nil:
			found.problems = append(found.problems, problem{root, err.Error()})
		case info.IsDir():
			found.walk(root)
		default:
			found.add(root)
		}
	}
	files := make([]string, 0, len(found.seen))
	for path := range found.seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, found.problems
}

func (found *collector) walk(root string) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		return found.visit(root, path, entry, err)
	})
}

func (found *collector) visit(root, path string, entry fs.DirEntry, err error) error {
	if err != nil {
		found.problems = append(found.problems, problem{path, err.Error()})
		return nil
	}
	if entry.IsDir() {
		if path != root && skipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	found.add(path)
	return nil
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata"
}

func (found *collector) add(path string) {
	name := filepath.Base(path)
	if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
		found.seen[path] = true
	}
}

// ---------------------------------------------------------------------------
// Measurement
// ---------------------------------------------------------------------------

func relPath(path, cwd string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

func analyzeFiles(files []string, cwd string) ([]unit, []problem) {
	var units []unit
	var problems []problem
	for _, path := range files {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		rel := relPath(path, cwd)
		if err != nil {
			problems = append(problems, problem{rel, "parse error: " + err.Error()})
			continue
		}
		units = append(units, fileUnits(&scope{fset: fset, rel: rel}, parsed)...)
	}
	return units, problems
}

func fileUnits(ctx *scope, parsed *ast.File) []unit {
	var units []unit
	for _, decl := range parsed.Decls {
		switch item := decl.(type) {
		case *ast.FuncDecl:
			if item.Body != nil {
				units = append(units, measureUnit(ctx, declName(item), item.Pos(), item.Body))
			}
		case *ast.GenDecl:
			units = append(units, globalLits(ctx, item)...)
		}
	}
	suffixDuplicates(units)
	return units
}

// globalLits measures the outermost function literals outside any function
// declaration, one unit each (numbered in source order per file).
func globalLits(ctx *scope, decl *ast.GenDecl) []unit {
	var units []unit
	for _, lit := range outermostLits(decl) {
		ctx.globals++
		name := fmt.Sprintf("glob.func%d", ctx.globals)
		units = append(units, measureUnit(ctx, name, lit.Pos(), lit.Body))
	}
	return units
}

// outermostLits returns the function literals in root that are not inside
// another function literal.
func outermostLits(root ast.Node) []*ast.FuncLit {
	var lits []*ast.FuncLit
	ast.Inspect(root, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if ok {
			lits = append(lits, lit)
		}
		return !ok
	})
	return lits
}

// suffixDuplicates renames the second and later units with the same name in
// one file to name#2, name#3 (several init or _ functions).
func suffixDuplicates(units []unit) {
	seen := map[string]int{}
	for index := range units {
		seen[units[index].name]++
		if count := seen[units[index].name]; count > 1 {
			units[index].name = fmt.Sprintf("%s#%d", units[index].name, count)
			units[index].key = units[index].file + "::" + units[index].name
		}
	}
}

// measureUnit returns the unit for body, scored over everything inside it.
func measureUnit(ctx *scope, name string, pos token.Pos, body *ast.BlockStmt) unit {
	return unit{key: ctx.rel + "::" + name, file: ctx.rel, name: name,
		line: ctx.fset.Position(pos).Line, score: measure(body), scoreV1: measureV1(body)}
}

// measure returns the McCabe score of root. It descends into function
// literals, so their branches count toward the unit that contains them.
func measure(root ast.Node) int {
	score := 1
	ast.Inspect(root, func(node ast.Node) bool {
		score += weight(node)
		return true
	})
	return score
}

// measureV1 is counting version 1: it does not descend into function
// literals. Read only by -migrate-baseline; removed in kit v3.0.
func measureV1(root ast.Node) int {
	score := 1
	ast.Inspect(root, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}
		score += weight(node)
		return true
	})
	return score
}

func weight(node ast.Node) int {
	switch item := node.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
		return 1
	case *ast.CaseClause:
		return boolWeight(len(item.List) > 0)
	case *ast.CommClause:
		return boolWeight(item.Comm != nil)
	case *ast.BinaryExpr:
		return boolWeight(item.Op == token.LAND || item.Op == token.LOR)
	}
	return 0
}

func boolWeight(counted bool) int {
	if counted {
		return 1
	}
	return 0
}

func declName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return decl.Name.Name
	}
	return receiverName(decl.Recv.List[0].Type) + "." + decl.Name.Name
}

// receiverName is the receiver type name without pointer or type parameters.
func receiverName(expr ast.Expr) string {
	for {
		switch item := expr.(type) {
		case *ast.StarExpr:
			expr = item.X
		case *ast.ParenExpr:
			expr = item.X
		case *ast.IndexExpr:
			expr = item.X
		case *ast.IndexListExpr:
			expr = item.X
		case *ast.Ident:
			return item.Name
		default:
			return "?"
		}
	}
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

func emit(rep report, asJSON bool, stdout, stderr io.Writer) {
	if rep.Violations == nil {
		rep.Violations = []string{}
	}
	if rep.Details == nil {
		rep.Details = []detail{}
	}
	if rep.Tightenable == nil {
		rep.Tightenable = []change{}
	}
	human := stdout
	if asJSON {
		human = stderr
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(rep)
	}
	for _, line := range rep.Violations {
		fmt.Fprintln(human, line)
	}
	printMigration(human, rep)
	fmt.Fprintf(human, "gate-ast: %d functions in %d files, %d violations, %d tightenable\n",
		rep.FunctionsChecked, rep.FilesChecked, len(rep.Violations), len(rep.Tightenable))
}

// ---------------------------------------------------------------------------
// Baseline migration, counting v1 -> v2 (G1 spec R4, R5). Removed in kit v3.0.
// ---------------------------------------------------------------------------

var genesisPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
var globPattern = regexp.MustCompile(`^glob\.func[0-9]+(#[0-9]+)?$`)
var suffixPattern = regexp.MustCompile(`#[0-9]+$`)

const pinsPath = ".crucible/pins.json"

// family is a name without its order-dependent part: f, f#2 and f#3 are one
// family, and every glob.funcN of a file is one family. In Go keys the #N
// suffix is always the last part of the name.
func family(name string) string {
	if globPattern.MatchString(name) {
		return "glob.func"
	}
	return suffixPattern.ReplaceAllString(name, "")
}

type migrationRow struct {
	Key         string   `json:"key"`
	B1          *int     `json:"B1"`
	G1          *int     `json:"G1"`
	G           *int     `json:"G"`
	H           *int     `json:"H"`
	Allowed     *int     `json:"allowed,omitempty"`
	New         *int     `json:"new"`
	Reason      string   `json:"reason"`
	Flags       []string `json:"flags"`
	GenesisLine *int     `json:"genesis_line"`
	HeadLine    *int     `json:"head_line"`
}

type migrationPlan struct {
	candidate *baselineFile
	rows      []migrationRow
	genesis   string
	engine    string
	notes     []string
	dirty     bool
	files     int
	functions int
}

func intPtr(value int) *int { return &value }

func show(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}

func (row migrationRow) text() string {
	return fmt.Sprintf("migration %s: B1=%s G1=%s G=%s H=%s new=%s reason=%s lines=%s/%s %s",
		row.Key, show(row.B1), show(row.G1), show(row.G), show(row.H), show(row.New),
		row.Reason, show(row.GenesisLine), show(row.HeadLine), strings.Join(row.Flags, " "))
}

// printMigration writes the lines a human checks (spec R5.4): each row, the
// genesis, the engine hash, each genesis note and the dirty flag.
func printMigration(human io.Writer, rep report) {
	if rep.MigrationInfo == nil {
		return
	}
	for _, row := range rep.Migration {
		fmt.Fprintln(human, row.text())
	}
	fmt.Fprintf(human, "migration genesis: %s\n", rep.Genesis)
	fmt.Fprintf(human, "migration engine sha256: %s\n", rep.EngineSHA256)
	for _, note := range rep.GenesisNotes {
		fmt.Fprintf(human, "migration genesis note: %s\n", note)
	}
	dirty := "no"
	if rep.Dirty {
		dirty = "yes"
	}
	fmt.Fprintf(human, "migration work tree dirty: %s\n", dirty)
}

func migrateBaseline(opts options) (report, int) {
	plan, err := buildMigration(opts)
	if err != nil {
		return errorReport(err.Error()), exitError
	}
	if opts.verifyMigration {
		return verifyMigration(opts, plan)
	}
	if err := plan.candidate.save(opts.baseline, false); err != nil {
		return errorReport(err.Error()), exitError
	}
	rep, code := execute(options{limit: opts.limit, baseline: opts.baseline, asJSON: opts.asJSON, paths: opts.paths})
	plan.attach(&rep, "migrate-baseline")
	rep.BaselineWritten = opts.baseline
	return rep, code
}

func (plan *migrationPlan) attach(rep *report, action string) {
	rows, notes := plan.rows, plan.notes
	if rows == nil {
		rows = []migrationRow{}
	}
	if notes == nil {
		notes = []string{}
	}
	rep.MigrationInfo = &MigrationInfo{Action: action, Genesis: plan.genesis, EngineSHA256: plan.engine,
		Migration: rows, GenesisNotes: notes, Dirty: plan.dirty}
}

func buildMigration(opts options) (*migrationPlan, error) {
	if err := checkEngineFolder(); err != nil {
		return nil, err
	}
	if err := refuseUnsoundRepository(); err != nil {
		return nil, err
	}
	b1, err := loadV1Source(opts)
	if err != nil {
		return nil, err
	}
	genesis, err := readGenesis()
	if err != nil {
		return nil, err
	}
	g, notes, err := measureGenesis(genesis, opts.paths)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	h, _, hFiles := measureAt(cwd, opts.paths)
	functions, rows, err := migrate(b1.functions, g, h, opts.limit)
	if err != nil {
		return nil, err
	}
	plan, err := newMigrationPlan(b1, functions, rows, genesis, notes)
	if err == nil {
		plan.files, plan.functions = hFiles, len(h)
	}
	return plan, err
}

func loadV1Source(opts options) (*baselineFile, error) {
	if opts.verifyMigration {
		return committedV1(opts.baseline)
	}
	base, err := loadBaselineVersion(opts.baseline, 1)
	if err == nil && !base.exists {
		return nil, fmt.Errorf("no baseline at %s: nothing to migrate", opts.baseline)
	}
	return base, err
}

// ---- git access: raw objects only, replace refs off ----

// git runs one git command with replace refs disabled, so only real objects
// are read, and the commit-graph file is ignored (it can fake ancestry). It returns stdout; a failed run returns an error carrying stderr.
func git(stdin []byte, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-c", "core.commitGraph=false"}, args...)...)
	command.Env = gitEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var exit *exec.ExitError
	err := command.Run()
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case errors.As(err, &exit):
		return stdout.Bytes(), errors.New(strings.TrimSpace(stderr.String()))
	}
	return nil, fmt.Errorf("cannot run git: %v", err)
}

// gitEnvironment is the parent environment without any GIT_* variable (a
// parent GIT_DIR would redirect every read to another repository), plus the
// switches that turn replace refs and the lazy fetch of a missing object off.
func gitEnvironment() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if len(entry) < 4 || !strings.EqualFold(entry[:4], "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1")
}

// absentError marks a revision or a path that is honestly not there, as
// opposed to a damaged object store.
type absentError struct{ message string }

func (e *absentError) Error() string { return e.message }

// missingError marks an object that git has no record of, although an
// authentic object names it.
type missingError struct{ message string }

func (e *missingError) Error() string { return e.message }

// object is one re-hashed reply of `git cat-file --batch`.
type object struct {
	id   string
	kind string
	data []byte
}

var objectKinds = map[string]bool{"blob": true, "tree": true, "commit": true, "tag": true}

const (
	typeMask      = 0o170000
	directoryType = 0o040000
	fileType      = 0o100000
)

// objectID returns the id git gives an object of the given kind and content:
// SHA-1 for a 40-digit id, SHA-256 for a 64-digit one.
func objectID(kind string, data []byte, width int) (string, error) {
	payload := append([]byte(fmt.Sprintf("%s %d\x00", kind, len(data))), data...)
	switch width {
	case 40:
		sum := sha1.Sum(payload)
		return hex.EncodeToString(sum[:]), nil
	case 64:
		sum := sha256.Sum256(payload)
		return hex.EncodeToString(sum[:]), nil
	}
	return "", fmt.Errorf("an object id of %d hex digits is neither SHA-1 nor SHA-256", width)
}

// nextObject splits one `cat-file --batch` reply off data and re-hashes it:
// git does not notice a loose object that was rewritten in place.
func nextObject(data []byte, spec string) (object, []byte, error) {
	end := bytes.IndexByte(data, '\n')
	if end < 0 {
		return object{}, nil, &missingError{"object " + spec + " is not in this repository"}
	}
	header := strings.Fields(string(data[:end]))
	if len(header) != 3 || !objectKinds[header[1]] {
		return object{}, nil, &missingError{"object " + spec + " is not in this repository"}
	}
	size, err := strconv.Atoi(header[2])
	if err != nil || size < 0 || end+1+size > len(data) {
		return object{}, nil, fmt.Errorf("object %s has a malformed reply", spec)
	}
	item := object{id: header[0], kind: header[1], data: data[end+1 : end+1+size]}
	want, err := objectID(item.kind, item.data, len(item.id))
	if err != nil {
		return object{}, nil, err
	}
	if want != item.id {
		return object{}, nil, fmt.Errorf("object %s does not hash to its id: the object store was changed in place (forged or corrupt)", spec)
	}
	return item, bytes.TrimPrefix(data[end+1+size:], []byte("\n")), nil
}

// readObjects returns the re-hashed objects named by specs (object ids), in
// order, from one `git cat-file --batch` (no filters, no textconv).
func readObjects(specs []string) ([]object, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	data, err := git([]byte(strings.Join(specs, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, fmt.Errorf("git cat-file failed: %v", err)
	}
	objects := make([]object, 0, len(specs))
	for _, spec := range specs {
		item, rest, err := nextObject(data, spec)
		if err != nil {
			return nil, err
		}
		objects, data = append(objects, item), rest
	}
	return objects, nil
}

// readKind returns the re-hashed contents of oids, each of which must be an
// object of the given kind. The ids come from an authentic object, so an
// absent one is damage, never "no such path".
func readKind(oids []string, kind string) ([][]byte, error) {
	objects, err := readObjects(oids)
	if err != nil {
		return nil, err
	}
	contents := make([][]byte, 0, len(oids))
	for index, item := range objects {
		if item.id != oids[index] || item.kind != kind {
			return nil, fmt.Errorf("object %s is a %s, expected a %s", oids[index], item.kind, kind)
		}
		contents = append(contents, item.data)
	}
	return contents, nil
}

// commitTree returns the root tree id of a commit object.
func commitTree(rev string, item object) (string, error) {
	if item.kind != "commit" {
		return "", fmt.Errorf("%s names %s %s: it is not a commit object (a tag?)", rev, item.kind, item.id)
	}
	first, _, _ := bytes.Cut(item.data, []byte("\n"))
	tree := strings.TrimPrefix(string(first), "tree ")
	if tree == string(first) || !genesisPattern.MatchString(tree) {
		return "", fmt.Errorf("commit %s has no tree header", item.id)
	}
	return tree, nil
}

// resolveCommit returns the root tree id of the commit rev names. There is
// no ^{commit} peel: git would print nothing for a commit that was rewritten
// in place, which would read as "absent". The commit is read and re-hashed.
func resolveCommit(rev, label string) (string, error) {
	out, err := git(nil, "rev-parse", "--verify", "-q", rev)
	if err != nil && err.Error() != "" {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !genesisPattern.MatchString(oid) {
		return "", &absentError{rev + " does not name a commit"}
	}
	objects, err := readObjects([]string{oid})
	var missing *missingError
	if errors.As(err, &missing) {
		if label == "" {
			label = rev
		}
		return "", &missingError{label + " is not in this repository"}
	}
	if err != nil {
		return "", err
	}
	return commitTree(rev, objects[0])
}

// treeRecord is one entry of a raw tree object.
type treeRecord struct {
	mode uint64
	name string
	oid  string
}

// nextTreeRecord splits one `<mode> <name>\0<raw oid>` record off data.
func nextTreeRecord(data []byte, raw int) (treeRecord, []byte, error) {
	space := bytes.IndexByte(data, ' ')
	if space < 0 {
		return treeRecord{}, nil, errors.New("a tree object is malformed")
	}
	nul := bytes.IndexByte(data[space+1:], 0)
	if nul < 0 || space+1+nul+1+raw > len(data) {
		return treeRecord{}, nil, errors.New("a tree object is malformed")
	}
	end := space + 1 + nul
	mode, err := strconv.ParseUint(string(data[:space]), 8, 64)
	if err != nil {
		return treeRecord{}, nil, errors.New("a tree object has a malformed entry mode")
	}
	name := string(data[space+1 : end])
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return treeRecord{}, nil, fmt.Errorf("unsafe path in a tree: %q is no plain name", name)
	}
	oid := hex.EncodeToString(data[end+1 : end+1+raw])
	return treeRecord{mode: mode, name: name, oid: oid}, data[end+1+raw:], nil
}

// parseTree returns every record of a raw tree. A cut-short record, a mode
// that is not octal digits, a name that is no plain path component and a name
// that occurs twice are errors: git reads the first duplicate, a writer would
// use the last.
func parseTree(data []byte, width int) ([]treeRecord, error) {
	var records []treeRecord
	seen := map[string]bool{}
	for len(data) > 0 {
		record, rest, err := nextTreeRecord(data, width/2)
		if err != nil {
			return nil, err
		}
		if seen[record.name] {
			return nil, fmt.Errorf("a tree names %q more than once", record.name)
		}
		seen[record.name] = true
		records, data = append(records, record), rest
	}
	return records, nil
}

// pathEntry is the type bits and the object id of one tree entry.
type pathEntry struct {
	kind uint64
	oid  string
}

// childEntry finds the entry called name in the tree; nil when it is absent.
func childEntry(tree string, width int, name string) (*pathEntry, error) {
	contents, err := readKind([]string{tree}, "tree")
	if err != nil {
		return nil, err
	}
	records, err := parseTree(contents[0], width)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.name == name {
			return &pathEntry{kind: record.mode & typeMask, oid: record.oid}, nil
		}
	}
	return nil, nil
}

// walkTree returns the entry at parts below the tree root, or nil when a
// component is absent or a file stands where a directory is needed. Every
// tree on the way is read and re-hashed: a rewritten tree could point a path
// at another, correctly hashed, blob.
func walkTree(root string, parts []string) (*pathEntry, error) {
	entry := &pathEntry{kind: directoryType, oid: root}
	for _, part := range parts {
		if entry.kind != directoryType {
			return nil, nil
		}
		next, err := childEntry(entry.oid, len(root), part)
		if err != nil || next == nil {
			return nil, err
		}
		entry = next
	}
	return entry, nil
}

// prefixParts is the path from the repository root down to the working
// directory, as components (empty at the root).
func prefixParts() ([]string, error) {
	prefix, err := gitAnswer("rev-parse", "--show-prefix")
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, part := range strings.Split(prefix, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts, nil
}

func climbsOut(clean string) bool {
	return clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || filepath.IsAbs(clean)
}

// lookupBlob walks the tree to the cleaned path below the working directory
// and reads the blob there when the entry is a file.
func lookupBlob(tree, clean string) (*pathEntry, []byte, error) {
	parts, err := prefixParts()
	if err != nil {
		return nil, nil, err
	}
	for _, part := range strings.Split(clean, "/") {
		if part != "." {
			parts = append(parts, part)
		}
	}
	entry, err := walkTree(tree, parts)
	if err != nil || entry == nil || entry.kind != fileType {
		return entry, nil, err
	}
	contents, err := readKind([]string{entry.oid}, "blob")
	if err != nil {
		return nil, nil, err
	}
	return entry, contents[0], nil
}

// committedFile returns the raw, re-hashed bytes of HEAD:./file. The commit,
// the root tree, every tree on the path and the blob are each read through
// the re-hashing reader; git's own path lookup is never used. An absentError
// means HEAD or the path is not there; any other error is damage or a
// non-file.
func committedFile(file string) ([]byte, error) {
	clean := filepath.ToSlash(filepath.Clean(file))
	if climbsOut(clean) {
		return nil, fmt.Errorf("HEAD:%s lies outside the work tree", file)
	}
	tree, err := resolveCommit("HEAD", "")
	if err != nil {
		return nil, err
	}
	entry, blob, err := lookupBlob(tree, clean)
	var missing *missingError
	switch {
	case errors.As(err, &missing):
		return nil, fmt.Errorf("reading HEAD:%s: %v", file, err)
	case err != nil:
		return nil, err
	case entry == nil:
		return nil, &absentError{fmt.Sprintf("HEAD:%s does not exist", file)}
	case entry.kind != fileType:
		return nil, fmt.Errorf("HEAD:%s is not a file", file)
	}
	return blob, nil
}

func committedV1(path string) (*baselineFile, error) {
	label := "HEAD:" + filepath.ToSlash(filepath.Clean(path))
	data, err := committedFile(path)
	var absent *absentError
	if errors.As(err, &absent) {
		return nil, fmt.Errorf("cannot read the committed baseline %s: %v", label, err)
	}
	if err != nil {
		return nil, err
	}
	return parseBaseline(data, label, 1)
}

func pinsGenesis(data []byte, label string) (string, error) {
	var pins map[string]any
	if err := json.Unmarshal(data, &pins); err != nil {
		return "", fmt.Errorf("cannot read the genesis from %s: %v", label, err)
	}
	genesis, _ := pins["genesis"].(string)
	if !genesisPattern.MatchString(genesis) {
		return "", fmt.Errorf("%s has no valid genesis commit; the migration needs one (changing it is a repin, not a flag)", label)
	}
	return genesis, nil
}

// readGenesis returns the genesis commit from the committed pins.json. A
// work-tree copy that disagrees is refused (spec R6).
func readGenesis() (string, error) {
	data, err := committedFile(pinsPath)
	var absent *absentError
	if errors.As(err, &absent) {
		return "", fmt.Errorf("no committed %s at HEAD (the genesis is read from the commit, not the work tree): %v", pinsPath, err)
	}
	if err != nil {
		return "", err
	}
	genesis, err := pinsGenesis(data, "HEAD:"+pinsPath)
	if err != nil {
		return "", err
	}
	if err := checkWorkTreePins(genesis); err != nil {
		return "", err
	}
	return genesis, checkGenesisCommit(genesis)
}

func checkWorkTreePins(genesis string) error {
	data, err := os.ReadFile(filepath.FromSlash(pinsPath))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read %s: %v", pinsPath, err)
	}
	other, err := pinsGenesis(data, pinsPath)
	if err != nil {
		return err
	}
	if other != genesis {
		return fmt.Errorf("the genesis in %s differs from the committed one; commit and repin it first", pinsPath)
	}
	return nil
}

func refuseGrafts() error {
	located, err := git(nil, "rev-parse", "--git-path", "info/grafts")
	if err != nil {
		return fmt.Errorf("git rev-parse failed: %v", err)
	}
	grafts := strings.TrimSpace(string(located))
	if _, err := os.Stat(grafts); err == nil {
		return fmt.Errorf("%s exists: grafts can fake ancestry; remove it first", grafts)
	}
	return nil
}

// dangerousConfig matches the local config keys that redirect git or run code
// inside it (architecture 16.2).
var dangerousConfig = regexp.MustCompile(`(?i)^(core\.(worktree|hookspath|fsmonitor|sshcommand)|extensions\.(partialclone|worktreeconfig)|remote\..+\.promisor|filter\..+|diff\..+\.(textconv|command)|gpg\.program|gpg\..+\.program|include\.path|includeif\..+)$`)

func gitAnswer(args ...string) (string, error) {
	out, err := git(nil, args...)
	if err != nil {
		return "", fmt.Errorf("git %s failed: %v", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func realPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

// within reports whether dir is root or lies below it.
func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkToplevel refuses a working directory that lies outside the root git
// reports: a core.worktree setting would send every read to another tree. A
// directory below the root is fine.
func checkToplevel() error {
	top, err := gitAnswer("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	realTop, topErr := realPath(top)
	realCwd, cwdErr := realPath(cwd)
	if topErr != nil || cwdErr != nil {
		return fmt.Errorf("cannot resolve the work tree root %s or the working directory %s", top, cwd)
	}
	if !within(realTop, realCwd) {
		return fmt.Errorf("git reports the work tree root %s, which does not contain %s (a core.worktree setting redirects git); the migration reads only this tree", realTop, realCwd)
	}
	return nil
}

func checkShallow() error {
	answer, err := gitAnswer("rev-parse", "--is-shallow-repository")
	if err != nil {
		return err
	}
	if answer == "true" {
		return errors.New("this is a shallow clone (.git/shallow): hidden parents change ancestry answers; fetch full history (git fetch --unshallow)")
	}
	return nil
}

// localConfigKeys lists the keys of the local config and, when it exists, the
// worktree config (includes are not followed).
func localConfigKeys() ([]string, error) {
	scopes := []string{"--local"}
	located, err := gitAnswer("rev-parse", "--git-path", "config.worktree")
	if err != nil {
		return nil, err
	}
	if fileExists(located) {
		scopes = append(scopes, "--worktree")
	}
	var keys []string
	for _, scope := range scopes {
		listing, err := git(nil, "config", scope, "--list", "-z", "--no-includes")
		if err != nil {
			return nil, fmt.Errorf("git config %s --list failed: %v", scope, err)
		}
		for _, record := range strings.Split(string(listing), "\x00") {
			if key, _, _ := strings.Cut(record, "\n"); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}

func checkLocalConfig() error {
	keys, err := localConfigKeys()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var risky []string
	for _, key := range keys {
		if dangerousConfig.MatchString(key) && !seen[key] {
			seen[key] = true
			risky = append(risky, key)
		}
	}
	sort.Strings(risky)
	if len(risky) > 0 {
		return fmt.Errorf("local git config sets %s: each can run code or redirect git; remove it (git config --unset <key>) or work in a full clone", strings.Join(risky, ", "))
	}
	return nil
}

// refuseUnsoundRepository runs the A2 checks (architecture 16.2) before the
// first read: a redirected work tree, a shallow clone, a grafts file, and a
// local config key that redirects git or runs code.
func refuseUnsoundRepository() error {
	for _, check := range []func() error{checkToplevel, checkShallow, refuseGrafts, checkLocalConfig} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func checkGenesisCommit(genesis string) error {
	if err := refuseGrafts(); err != nil {
		return err
	}
	if _, err := resolveCommit("HEAD", ""); err != nil { // both commits are re-hashed before git reasons about them
		return err
	}
	if _, err := resolveCommit(genesis, "genesis "+genesis+" named in "+pinsPath); err != nil {
		return err
	}
	if _, err := git(nil, "merge-base", "--is-ancestor", genesis, "HEAD"); err != nil {
		return fmt.Errorf("genesis %s is not an ancestor of HEAD or is not available in the history: %v", genesis, err)
	}
	return nil
}

type treeEntry struct {
	sha  string
	path string
}

// levelTree is one directory of the level being listed.
type levelTree struct {
	prefix string
	oid    string
}

// classify adds the regular files of one tree to files and returns its
// directories. Files are told by their type bits, as ls-tree canonicalized
// them; symlinks and submodules are skipped.
func classify(prefix string, records []treeRecord, files *[]treeEntry) []levelTree {
	var below []levelTree
	for _, record := range records {
		path := record.name
		if prefix != "" {
			path = prefix + "/" + record.name
		}
		switch record.mode & typeMask {
		case directoryType:
			below = append(below, levelTree{prefix: path, oid: record.oid})
		case fileType:
			*files = append(*files, treeEntry{sha: record.oid, path: path})
		}
	}
	return below
}

// listLevel reads the trees of one level in one batch, collects their files
// and returns the directories of the next level.
func listLevel(level []levelTree, width int, files *[]treeEntry) ([]levelTree, error) {
	var oids []string
	known := map[string]bool{}
	for _, item := range level {
		if !known[item.oid] {
			known[item.oid] = true
			oids = append(oids, item.oid)
		}
	}
	contents, err := readKind(oids, "tree")
	if err != nil {
		return nil, err
	}
	byID := map[string][]byte{}
	for index, oid := range oids {
		byID[oid] = contents[index]
	}
	var below []levelTree
	for _, item := range level {
		records, err := parseTree(byID[item.oid], width)
		if err != nil {
			return nil, err
		}
		below = append(below, classify(item.prefix, records, files)...)
	}
	return below, nil
}

// treeEntries lists the regular files of commit below the working directory
// (as `git ls-tree -r` run from there lists them); symlinks and submodules
// are skipped. The trees are read raw and re-hashed, one batch per directory
// level, and parsed here: `git ls-tree` does not verify a loose object.
func treeEntries(commit string) ([]treeEntry, error) {
	root, err := resolveCommit(commit, "")
	if err != nil {
		return nil, err
	}
	prefix, err := prefixParts()
	if err != nil {
		return nil, err
	}
	start, err := walkTree(root, prefix)
	if err != nil || start == nil || start.kind != directoryType {
		return nil, err
	}
	var files []treeEntry
	for level := []levelTree{{oid: start.oid}}; len(level) > 0; {
		if level, err = listLevel(level, len(root), &files); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// readBlobs returns the raw, re-hashed contents of shas, in order.
func readBlobs(shas []string) ([][]byte, error) {
	return readKind(shas, "blob")
}

func safeRelative(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// extractTree writes the regular files of commit under target from raw git
// objects.
func extractTree(commit, target string) error {
	entries, err := treeEntries(commit)
	if err != nil {
		return genesisTreeError(err)
	}
	shas := make([]string, len(entries))
	for index, entry := range entries {
		shas[index] = entry.sha
	}
	blobs, err := readBlobs(shas)
	if err != nil {
		return genesisTreeError(err)
	}
	for index, entry := range entries {
		if err := writeMember(target, entry.path, blobs[index]); err != nil {
			return err
		}
	}
	return nil
}

// genesisTreeError says that a missing object belongs to the genesis tree.
func genesisTreeError(err error) error {
	var missing *missingError
	if errors.As(err, &missing) {
		return fmt.Errorf("the genesis tree names an object that is missing: %v", err)
	}
	return err
}

func writeMember(target, name string, content []byte) error {
	if !safeRelative(name) {
		return fmt.Errorf("unsafe path in the genesis tree: %s", name)
	}
	path := filepath.Join(target, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot extract %s: %v", name, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("cannot extract %s: %v", name, err)
	}
	return nil
}

func measureGenesis(genesis string, roots []string) (map[string]unit, []string, error) {
	target, err := os.MkdirTemp("", "crucible-genesis-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(target)
	if err := extractTree(genesis, target); err != nil {
		return nil, nil, err
	}
	units, notes, _ := measureAt(target, roots)
	return units, notes, nil
}

// measureAt measures roots inside base; keys are relative to base, so genesis
// and work-tree keys match. Missing roots and parse errors become notes.
func measureAt(base string, roots []string) (map[string]unit, []string, int) {
	var joined []string
	for _, root := range roots {
		joined = append(joined, filepath.Join(base, normalizeRoot(root)))
	}
	files, problems := collectFiles(joined)
	units, parseProblems := analyzeFiles(files, base)
	notes := []string{}
	prefix := base + string(filepath.Separator)
	for _, item := range append(problems, parseProblems...) {
		notes = append(notes, strings.ReplaceAll(item.file+": "+item.msg, prefix, ""))
	}
	out := map[string]unit{}
	for _, item := range units {
		out[item.key] = item
	}
	return out, notes, len(files)
}

// ---- rows ----

func migrate(b1 map[string]int, g, h map[string]unit, limit int) (map[string]int, []migrationRow, error) {
	unverified := changedFamilies(g, h)
	functions := map[string]int{}
	var rows []migrationRow
	for _, key := range reportKeys(b1, g, h, limit) {
		row, err := migrationRowFor(key, b1, g, h, limit, unverified)
		if err != nil {
			return nil, nil, err
		}
		if row.New != nil {
			functions[key] = *row.New
		}
		rows = append(rows, row)
	}
	return functions, rows, nil
}

func reportKeys(b1 map[string]int, g, h map[string]unit, limit int) []string {
	set := map[string]bool{}
	for key := range b1 {
		set[key] = true
	}
	for _, units := range []map[string]unit{g, h} {
		for key, item := range units {
			if item.score > limit {
				set[key] = true
			}
		}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func familyKey(item unit) string {
	return item.file + "::" + family(item.name)
}

// changedFamilies returns the (file, family) pairs whose unit count differs
// between genesis and HEAD; their keys cannot be matched by position.
func changedFamilies(g, h map[string]unit) map[string]bool {
	gCount, hCount := familyCounts(g), familyCounts(h)
	out := map[string]bool{}
	for _, counts := range []map[string]int{gCount, hCount} {
		for key := range counts {
			if gCount[key] != hCount[key] {
				out[key] = true
			}
		}
	}
	return out
}

func familyCounts(units map[string]unit) map[string]int {
	counts := map[string]int{}
	for _, item := range units {
		counts[familyKey(item)]++
	}
	return counts
}

func migrationRowFor(key string, b1 map[string]int, g, h map[string]unit, limit int, unverified map[string]bool) (migrationRow, error) {
	gUnit, atGenesis := g[key]
	hUnit, atHead := h[key]
	row := migrationRow{Key: key, Flags: []string{}, Reason: rowReason(gUnit, atGenesis, atHead, unverified)}
	fillMeasurements(&row, b1, gUnit, atGenesis, hUnit, atHead)
	if _, approved := b1[key]; approved && !atGenesis {
		row.Flags = append(row.Flags, adoptionFlag(key, g))
	}
	if row.Reason != "candidate" {
		return row, nil
	}
	return row, grandfather(&row, approvedValue(b1, key, limit), limit)
}

var nestedV1Pattern = regexp.MustCompile(`^(.+)\.func[0-9]+$`)

// adoptionFlag explains a baseline key that has no genesis unit. A version-1
// nested-literal key (Outer.funcN, glob.funcN.funcM) did exist at genesis,
// folded into its parent unit under counting v2.
func adoptionFlag(key string, g map[string]unit) string {
	path, name, _ := strings.Cut(key, "::")
	// Strip trailing .funcN parts one at a time: Legacy.func1.func1 is counted in Legacy.
	for !globPattern.MatchString(name) {
		match := nestedV1Pattern.FindStringSubmatch(name)
		if match == nil {
			break
		}
		name = match[1]
		if _, found := g[path+"::"+name]; found {
			return fmt.Sprintf("v1 nested key; now counted in %s::%s", path, name)
		}
	}
	return "grandfathered after adoption; must now meet the limit"
}

func rowReason(gUnit unit, atGenesis, atHead bool, unverified map[string]bool) string {
	switch {
	case !atGenesis:
		return "not at genesis"
	case !atHead:
		return "not at HEAD"
	case unverified[familyKey(gUnit)]:
		return "identity unverified"
	}
	return "candidate"
}

func fillMeasurements(row *migrationRow, b1 map[string]int, gUnit unit, atGenesis bool, hUnit unit, atHead bool) {
	if value, ok := b1[row.Key]; ok {
		row.B1 = intPtr(value)
	}
	if atGenesis {
		row.G, row.G1, row.GenesisLine = intPtr(gUnit.score), intPtr(gUnit.scoreV1), intPtr(gUnit.line)
	}
	if atHead {
		row.H, row.HeadLine = intPtr(hUnit.score), intPtr(hUnit.line)
	}
}

func approvedValue(b1 map[string]int, key string, limit int) int {
	if value, ok := b1[key]; ok {
		return value
	}
	return limit
}

func minInt(first int, rest ...int) int {
	for _, value := range rest {
		if value < first {
			first = value
		}
	}
	return first
}

// checkBound fails when value exceeds any of its bounds (G, H, allowed).
func checkBound(key string, value int, bounds ...int) error {
	for _, bound := range bounds {
		if value > bound {
			return fmt.Errorf("migration bound violated for %s", key)
		}
	}
	return nil
}

// grandfather applies new = min(G, H, approved + closure part at genesis) and
// flags a value above the last approved one (spec R4, R5).
func grandfather(row *migrationRow, approved, limit int) error {
	allowed := approved + (*row.G - *row.G1)
	value := minInt(*row.G, *row.H, allowed)
	row.Allowed = intPtr(allowed)
	if value <= limit {
		row.Reason = "within limit"
		return nil
	}
	if err := checkBound(row.Key, value, *row.G, *row.H, allowed); err != nil {
		return err
	}
	row.New, row.Reason = intPtr(value), "grandfathered"
	if value > approved {
		row.Flags = append(row.Flags, fmt.Sprintf("RAISED +%d", value-approved))
	}
	return nil
}

// ---- candidate and verification ----

func newMigrationPlan(b1 *baselineFile, functions map[string]int, rows []migrationRow, genesis string, notes []string) (*migrationPlan, error) {
	engine, err := engineSHA256()
	if err != nil {
		return nil, err
	}
	b1.functions = functions
	provenance, err := json.Marshal(map[string]any{"version": baselineVersion, "genesis_commit": genesis,
		"engine_sha256": engine, "timestamp": time.Now().UTC().Format("2006-01-02T15:04:05Z")})
	if err != nil {
		return nil, err
	}
	b1.raw["_provenance"] = provenance
	dirty, err := workTreeDirty()
	if err != nil {
		return nil, err
	}
	return &migrationPlan{candidate: b1, rows: rows, genesis: genesis, engine: engine, notes: notes, dirty: dirty}, nil
}

func engineSource() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot locate the engine source")
	}
	return source, nil
}

// checkEngineFolder refuses to migrate when the engine folder holds anything
// besides the engine source: `go run` compiles every .go file there, but the
// engine hash covers main.go only.
func checkEngineFolder() error {
	source, err := engineSource()
	if err != nil {
		return err
	}
	folder := filepath.Dir(source)
	entries, err := os.ReadDir(folder)
	if err != nil {
		return fmt.Errorf("cannot list the engine folder %s: %v", folder, err)
	}
	var extra []string
	for _, entry := range entries {
		if entry.Name() != filepath.Base(source) {
			extra = append(extra, entry.Name())
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("the engine folder %s holds %s besides %s; the kit installs only main.go there; remove the extra entries",
			folder, strings.Join(extra, ", "), filepath.Base(source))
	}
	return nil
}

func engineSHA256() (string, error) {
	source, err := engineSource()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("cannot read the engine source %s: %v", source, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func workTreeDirty() (bool, error) {
	out, err := git(nil, "-c", "core.fsmonitor=false", "status", "--porcelain", "--ignore-submodules=all")
	if err != nil {
		return false, fmt.Errorf("git status failed: %v", err)
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

func verifyMigration(opts options, plan *migrationPlan) (report, int) {
	staged, err := loadBaseline(opts.baseline)
	if err == nil && !staged.exists {
		err = fmt.Errorf("no staged baseline at %s to verify", opts.baseline)
	}
	if err != nil {
		return errorReport(err.Error()), exitError
	}
	rep := errorReport("")
	rep.Violations = baselineDifferences(staged, plan.candidate)
	rep.OK = len(rep.Violations) == 0
	rep.FilesChecked, rep.FunctionsChecked = plan.files, plan.functions
	plan.attach(&rep, "verify-migration")
	if !rep.OK {
		return rep, exitViolations
	}
	return rep, exitPass
}

func baselineDifferences(staged, candidate *baselineFile) []string {
	if reflect.DeepEqual(normalized(staged), normalized(candidate)) {
		return []string{}
	}
	return []string{"staged baseline differs from the re-derived migration"}
}

// normalized decodes a baseline with its functions map applied and without
// _provenance.timestamp, for comparison.
func normalized(base *baselineFile) map[string]any {
	out := map[string]any{}
	for key, raw := range base.raw {
		var value any
		_ = json.Unmarshal(raw, &value)
		out[key] = value
	}
	functions, _ := json.Marshal(base.functions)
	var decoded any
	_ = json.Unmarshal(functions, &decoded)
	out["functions"], out["version"] = decoded, float64(baselineVersion)
	if provenance, ok := out["_provenance"].(map[string]any); ok {
		delete(provenance, "timestamp")
	}
	return out
}
