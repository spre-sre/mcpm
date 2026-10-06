// Tier 2 telemetry driver (architecture.md section 8).
//
//	go run harness/telemetry_driver/main.go --root DIR --samples N --seed S --out FILE.json
//
// writes {"latencies_ns": [N floats], "memory_bytes": [N floats]}.
//
// The workload covers the paths the contract tests cover: DetectAndBuild on a
// valid manifest, on a directory with no marker and on an invalid manifest;
// GetServerPath on an installed and a missing name; ListServers. The six
// operations run in a fixed rotation, so every seed runs the same code paths;
// the seed picks only names, manifest contents and which fixture each call
// uses. No fixture makes DetectAndBuild run an external program.
//
// Each sample times a fixed batch of calls and reports the per-call mean
// (architecture.md 12.8). The batch is a constant multiple of the rotation, so
// both sides run the same operation mix per sample; it is not calibrated from
// the clock. One batch takes about 2.7 ms (minimum 200 us). Memory is the live
// heap after a forced GC, read outside the timed region.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
)

const (
	batch        = 120 // calls per sample; a multiple of len(operations)
	fixtureCount = 32  // fixtures of each kind
	workloadSize = 600 // planned steps; a multiple of batch
)

// fixtures holds the inputs prepared before any timing.
type fixtures struct {
	manifestDirs []string // valid mcp.json with an empty buildCmd
	emptyDirs    []string // no project marker
	invalidDirs  []string // mcp.json that is not valid JSON
	installed    []string // names under workspace/.mcp/servers
	missing      []string // names not under workspace/.mcp/servers
}

// operation is one call to a project entry point; rejects are valid work.
type operation func(f *fixtures, index int) error

var operations = []operation{
	func(f *fixtures, i int) error { _, err := builder.DetectAndBuild(f.manifestDirs[i]); return err },
	func(f *fixtures, i int) error { _, err := builder.DetectAndBuild(f.emptyDirs[i]); return err },
	func(f *fixtures, i int) error { _, err := builder.DetectAndBuild(f.invalidDirs[i]); return err },
	func(f *fixtures, i int) error { _, err := fetcher.GetServerPath(f.installed[i]); return err },
	func(f *fixtures, i int) error { _, err := fetcher.GetServerPath(f.missing[i]); return err },
	func(f *fixtures, i int) error { _, err := fetcher.ListServers(); return err },
}

// step is one planned call: which operation and which fixture.
type step struct {
	op      int
	fixture int
}

var sink int // keeps call results observable

// name returns a lowercase name that is a single path component.
func name(rng *rand.Rand) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := []byte{'s'}
	for length := 4 + rng.Intn(12); len(out) < length; {
		out = append(out, letters[rng.Intn(len(letters))])
	}
	return string(out)
}

// writeFixture creates dir/<file> with content and returns dir.
func writeFixture(dir, file string, content []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if file == "" {
		return dir, nil
	}
	return dir, os.WriteFile(filepath.Join(dir, file), content, 0o644)
}

// manifestJSON returns a seeded manifest with an empty buildCmd.
func manifestJSON(rng *rand.Rand) []byte {
	args := make([]string, 1+rng.Intn(6))
	for i := range args {
		args[i] = "/opt/" + name(rng)
	}
	data, _ := json.Marshal(builder.Manifest{Type: "node", RunCmd: name(rng), Args: args, RequiredEnv: []string{"API_KEY"}})
	return data
}

// buildFixtures creates every fixture under base from the seed.
func buildFixtures(base string, rng *rand.Rand) (*fixtures, error) {
	f := &fixtures{}
	taken := map[string]bool{}
	unique := func() string {
		for {
			if n := name(rng); !taken[n] {
				taken[n] = true
				return n
			}
		}
	}
	for i := 0; i < fixtureCount; i++ {
		valid := manifestJSON(rng)
		dirs := []struct {
			list    *[]string
			file    string
			content []byte
		}{
			{&f.manifestDirs, "mcp.json", valid},
			{&f.emptyDirs, "README.md", valid},
			{&f.invalidDirs, "mcp.json", valid[:rng.Intn(len(valid))]},
		}
		for kind, d := range dirs {
			dir, err := writeFixture(filepath.Join(base, "projects", fmt.Sprintf("%d-%d", kind, i)), d.file, d.content)
			if err != nil {
				return nil, err
			}
			*d.list = append(*d.list, dir)
		}
		installedName := unique()
		if _, err := writeFixture(filepath.Join(base, "workspace", ".mcp", "servers", installedName), "", nil); err != nil {
			return nil, err
		}
		f.installed = append(f.installed, installedName)
		f.missing = append(f.missing, unique())
	}
	return f, nil
}

// plan returns the workload: a fixed operation rotation, seeded fixtures.
func plan(rng *rand.Rand) []step {
	steps := make([]step, workloadSize)
	for i := range steps {
		steps[i] = step{op: i % len(operations), fixture: rng.Intn(fixtureCount)}
	}
	return steps
}

// runBatch runs count steps from start (wrapping) and returns the elapsed nanoseconds.
func runBatch(f *fixtures, steps []step, start, count int) float64 {
	begin := time.Now()
	for k := 0; k < count; k++ {
		s := steps[(start+k)%len(steps)]
		if operations[s.op](f, s.fixture) != nil {
			sink++
		}
	}
	return float64(time.Since(begin).Nanoseconds())
}

// measure prepares the fixtures, then takes the samples.
func measure(samples int, seed int64) (map[string][]float64, error) {
	base, err := os.MkdirTemp("", "mcpm-telemetry-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(base)

	rng := rand.New(rand.NewSource(seed))
	f, err := buildFixtures(base, rng)
	if err != nil {
		return nil, err
	}
	steps := plan(rng)
	// fetcher resolves .mcp/servers from the working directory.
	if err := os.Chdir(filepath.Join(base, "workspace")); err != nil {
		return nil, err
	}

	latencies := make([]float64, 0, samples)
	memory := make([]float64, 0, samples)
	var stats runtime.MemStats
	runBatch(f, steps, 0, len(steps)) // warm-up; not measured
	for i := 0; i < samples; i++ {
		latencies = append(latencies, runBatch(f, steps, i*batch, batch)/float64(batch))
		runtime.GC()
		runtime.ReadMemStats(&stats)
		memory = append(memory, float64(stats.HeapAlloc))
	}
	return map[string][]float64{"latencies_ns": latencies, "memory_bytes": memory}, nil
}

func run() error {
	root := flag.String("root", ".", "tree being measured (the command runs with cwd=root)")
	samples := flag.Int("samples", 0, "number of samples")
	seed := flag.Int64("seed", 0, "workload seed")
	out := flag.String("out", "", "output JSON file")
	flag.Parse()
	_ = root
	if *samples <= 0 || *out == "" {
		return errors.New("usage: --samples N (> 0) and --out FILE are required")
	}
	output, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	document, err := measure(*samples, *seed)
	if err != nil {
		return err
	}
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	return os.WriteFile(output, data, 0o644)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "telemetry_driver:", err)
		os.Exit(2)
	}
}
