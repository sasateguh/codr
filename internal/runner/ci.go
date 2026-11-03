// Package runner is codr-runner (D89): a small CI runner outside the forge. It receives push webhooks (HMAC checked),
// fetches the pushed commit, runs the jobs of .codr/ci.toml in locked-down containers and reports each job as a
// commit status through the API (D81), with a log page whose address is the status link.
package runner

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// CIPath is where a repository describes its jobs.
const CIPath = ".codr/ci.toml"

// File is .codr/ci.toml. Example:
//
//	image = "golang:1.23"            # default image of every job
//	branches = ["main", "release/*"] # push filter (path.Match globs); empty = every branch
//
//	[jobs.test]
//	run = ["go vet ./...", "go test ./..."]
//	timeout_minutes = 10             # capped by the runner's own limit
//
//	[jobs.lint]
//	image = "golangci/golangci-lint:v2.5"
//	run = ["golangci-lint run"]
type File struct {
	Image    string         `toml:"image"`
	Branches []string       `toml:"branches"`
	Jobs     map[string]Job `toml:"jobs"`
}

// Job is one job: its steps run in order in one container, stopping at the first failure.
type Job struct {
	Name           string   `toml:"-"`
	Image          string   `toml:"image"`
	Run            []string `toml:"run"`
	TimeoutMinutes int      `toml:"timeout_minutes"`
}

var (
	jobNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)
	// an image reference: registry/name:tag or @sha256 digest; no spaces, no option-looking values
	imageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,199}$`)
)

// ErrNoJobs: the file exists but lists nothing to run.
var ErrNoJobs = errors.New("no jobs")

// Parse reads and validates a ci.toml. Unknown keys are an error, so a typo is reported instead of silently ignored.
func Parse(b []byte, maxJobs, maxSteps int) (File, []Job, error) {
	var f File
	dec := toml.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			var keys []string
			for _, e := range strict.Errors {
				keys = append(keys, strings.Join(e.Key(), "."))
			}
			return f, nil, fmt.Errorf("%s: unknown key %s", CIPath, strings.Join(keys, ", "))
		}
		return f, nil, fmt.Errorf("%s: %w", CIPath, err)
	}
	if len(f.Jobs) == 0 {
		return f, nil, ErrNoJobs
	}
	if len(f.Jobs) > maxJobs {
		return f, nil, fmt.Errorf("%s: %d jobs, the runner allows %d", CIPath, len(f.Jobs), maxJobs)
	}
	for _, g := range f.Branches {
		if _, err := path.Match(g, ""); err != nil {
			return f, nil, fmt.Errorf("%s: branch pattern %q: %w", CIPath, g, err)
		}
	}
	var jobs []Job
	for name, j := range f.Jobs {
		j.Name = name
		if j.Image == "" {
			j.Image = f.Image
		}
		switch {
		case !jobNameRE.MatchString(name):
			return f, nil, fmt.Errorf("%s: job name %q: letters, digits, . _ - (max 40)", CIPath, name)
		case j.Image == "" || !imageRE.MatchString(j.Image):
			return f, nil, fmt.Errorf("%s: job %q: image %q is missing or not an image reference", CIPath, name, j.Image)
		case len(j.Run) == 0 || len(j.Run) > maxSteps:
			return f, nil, fmt.Errorf("%s: job %q: run needs 1 to %d steps", CIPath, name, maxSteps)
		case j.TimeoutMinutes < 0:
			return f, nil, fmt.Errorf("%s: job %q: timeout_minutes must be positive", CIPath, name)
		}
		for _, st := range j.Run {
			if strings.TrimSpace(st) == "" {
				return f, nil, fmt.Errorf("%s: job %q: an empty step", CIPath, name)
			}
		}
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].Name < jobs[b].Name })
	return f, jobs, nil
}

// Wants reports whether a push to branch runs this file.
func (f File) Wants(branch string) bool {
	if len(f.Branches) == 0 {
		return true
	}
	for _, g := range f.Branches {
		if ok, _ := path.Match(g, branch); ok {
			return true
		}
	}
	return false
}

// Script is the shell program of a job: every step echoed as "$ step" then run, the first failure ends it.
func (j Job) Script() string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, st := range j.Run {
		b.WriteString("printf '%s\\n' " + shellQuote("$ "+st) + "\n")
		b.WriteString(st + "\n")
	}
	return b.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
