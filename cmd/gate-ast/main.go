// Command gate-ast is the crucible AST gate for Go: a McCabe complexity limit
// with a baseline ratchet. It uses the Go standard library only.
//
// Usage: go run ./cmd/gate-ast [-json] [-limit N] [-baseline PATH]
//
//	[-snapshot | -tighten] [dir|file|dir/...]...
//
// Counting (base 1 per unit): +1 for each if, for, range, non-default case
// and non-default select case, and each && or ||. Every function declaration
// and every function literal is a separate unit; a literal is not counted
// into its enclosing function. Exit codes: 0 pass, 1 violations, 2 error.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	baselineVersion = 1
	exitPass        = 0
	exitViolations  = 1
	exitError       = 2
)

type options struct {
	limit    int
	baseline string
	snapshot bool
	tighten  bool
	asJSON   bool
	paths    []string
}

type unit struct {
	key   string
	file  string
	name  string
	line  int
	score int
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
	set.BoolVar(&opts.asJSON, "json", false, "print one JSON object on stdout")
	if err := set.Parse(args); err != nil {
		return opts, err
	}
	if opts.limit < 1 {
		return opts, fmt.Errorf("-limit must be at least 1")
	}
	if opts.snapshot && opts.tighten {
		return opts, fmt.Errorf("-snapshot and -tighten cannot be combined")
	}
	opts.paths = set.Args()
	if len(opts.paths) == 0 {
		opts.paths = []string{"."}
	}
	return opts, nil
}

func errorReport(msg string) report {
	return report{Violations: []string{msg}, Details: []detail{}, Tightenable: []change{}}
}

func execute(opts options) (report, int) {
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

func loadBaseline(path string) (*baselineFile, error) {
	base := &baselineFile{raw: map[string]json.RawMessage{}, functions: map[string]int{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return base, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read baseline %s: %v", path, err)
	}
	base.exists = true
	if err := json.Unmarshal(data, &base.raw); err != nil {
		return nil, fmt.Errorf("cannot read baseline %s: %v", path, err)
	}
	var version int
	if json.Unmarshal(base.raw["version"], &version) != nil || version != baselineVersion {
		return nil, fmt.Errorf("baseline %s: unsupported or missing version", path)
	}
	if raw, ok := base.raw["functions"]; ok {
		if err := json.Unmarshal(raw, &base.functions); err != nil || base.functions == nil {
			return nil, fmt.Errorf("baseline %s: 'functions' must map names to integers", path)
		}
	}
	return base, nil
}

func (base *baselineFile) save(path string, exclusive bool) error {
	functions, err := json.Marshal(base.functions)
	if err != nil {
		return err
	}
	base.raw["functions"] = functions
	if _, ok := base.raw["version"]; !ok {
		base.raw["version"] = json.RawMessage("1")
	}
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
				units = append(units, measureTree(ctx, declName(item), item.Pos(), item.Body)...)
			}
		case *ast.GenDecl:
			units = append(units, globalLits(ctx, item)...)
		}
	}
	return units
}

// globalLits measures function literals outside any function declaration.
func globalLits(ctx *scope, decl *ast.GenDecl) []unit {
	var units []unit
	_, lits := measure(decl)
	for _, lit := range lits {
		ctx.globals++
		name := fmt.Sprintf("glob.func%d", ctx.globals)
		units = append(units, measureTree(ctx, name, lit.Pos(), lit.Body)...)
	}
	return units
}

// measureTree returns the unit for body and, separately, one for each
// function literal inside it (numbered in source order).
func measureTree(ctx *scope, name string, pos token.Pos, body *ast.BlockStmt) []unit {
	score, lits := measure(body)
	units := []unit{{key: ctx.rel + "::" + name, file: ctx.rel, name: name,
		line: ctx.fset.Position(pos).Line, score: score}}
	for index, lit := range lits {
		inner := fmt.Sprintf("%s.func%d", name, index+1)
		units = append(units, measureTree(ctx, inner, lit.Pos(), lit.Body)...)
	}
	return units
}

// measure returns the McCabe score of root and its direct function literals,
// which it does not descend into.
func measure(root ast.Node) (int, []*ast.FuncLit) {
	score := 1
	var lits []*ast.FuncLit
	ast.Inspect(root, func(node ast.Node) bool {
		if lit, ok := node.(*ast.FuncLit); ok {
			lits = append(lits, lit)
			return false
		}
		score += weight(node)
		return true
	})
	return score, lits
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
	fmt.Fprintf(human, "gate-ast: %d functions in %d files, %d violations, %d tightenable\n",
		rep.FunctionsChecked, rep.FilesChecked, len(rep.Violations), len(rep.Tightenable))
}
