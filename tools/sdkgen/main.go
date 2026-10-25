package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// Default locations of the spec and the two committed generated files, relative
// to the repository root (which is where the Makefile runs the generator).
const (
	defaultSpec          = "api/openapi.yaml"
	defaultPythonOut     = "sdk/python/swarmcracker/types.py"
	defaultTypeScriptOut = "sdk/node/src/types.d.ts"
)

// target is one generated file: where it is committed and what it should hold.
type target struct {
	path string
	data []byte
}

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// run executes the generator and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sdkgen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	specPath := flags.String("spec", defaultSpec, "OpenAPI document to read")
	pythonPath := flags.String("out-python", defaultPythonOut, "generated Python model file")
	typescriptPath := flags.String("out-typescript", defaultTypeScriptOut, "generated TypeScript model file")
	check := flags.Bool("check", false, "report stale generated files instead of writing them")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "sdkgen: unexpected arguments: %v\n", flags.Args())
		return 2
	}

	targets, err := generate(*specPath, *pythonPath, *typescriptPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdkgen: %v\n", err)
		return 1
	}
	if *check {
		return checkTargets(targets, stderr)
	}
	if err := writeTargets(targets); err != nil {
		fmt.Fprintf(stderr, "sdkgen: %v\n", err)
		return 1
	}
	return 0
}

// generate resolves the spec and renders both model files in memory.
func generate(specPath, pythonPath, typescriptPath string) ([]target, error) {
	//nolint:gosec // G304: the spec path is an operator-supplied CLI flag.
	src, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}
	spec, err := Parse(src, specPath)
	if err != nil {
		return nil, err
	}
	python, err := GeneratePython(spec)
	if err != nil {
		return nil, err
	}
	typescript, err := GenerateTypeScript(spec)
	if err != nil {
		return nil, err
	}
	return []target{
		{path: pythonPath, data: python},
		{path: typescriptPath, data: typescript},
	}, nil
}

// checkTargets compares the committed files with freshly generated output. It
// never writes: a stale file is reported and the process exits non-zero.
func checkTargets(targets []target, stderr io.Writer) int {
	code := 0
	for _, item := range targets {
		//nolint:gosec // G304: the output path is an operator-supplied CLI flag.
		current, err := os.ReadFile(item.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(stderr, "%s: missing; run make sdk-gen\n", item.path)
			} else {
				fmt.Fprintf(stderr, "%s: %v\n", item.path, err)
			}
			code = 1
			continue
		}
		if string(current) != string(item.data) {
			fmt.Fprintf(stderr, "%s: out of date; run make sdk-gen\n", item.path)
			code = 1
		}
	}
	return code
}
