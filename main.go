package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	tool, args := os.Args[1], os.Args[2:]

	args, err := toolexec(tool, args...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cmd := exec.Command(tool, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printVersion(tool string, args ...string) {
	id, err := myBuildID()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd := exec.Command(tool, args...)
	stdout := bytes.NewBuffer(nil)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	v := strings.TrimSpace(stdout.String())
	fmt.Fprintf(os.Stdout, "%s +cover %s", v, id)
}

func toolexec(tool string, args ...string) ([]string, error) {
	toolName := filepath.Base(tool)
	if toolName != "compile" && toolName != "link" {
		return args, nil
	}

	if len(args) > 0 && args[0] == "-V=full" {
		printVersion(tool, args...)
		os.Exit(0)
	}

	switch toolName {
	case "compile":
		return compile(tool, args)
	case "link":
		return link(args)
	}

	return args, nil
}

var buildID string

func myBuildID() (string, error) {
	if buildID != "" {
		return buildID, nil
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	buildID, err = buildid(exe)
	if err != nil {
		return "", err
	}
	return buildID, nil
}

func buildid(path string) (string, error) {
	buf := bytes.NewBuffer(nil)
	cmd := exec.Command("go", "tool", "buildid", path)
	cmd.Stdout = buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func cacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	id, err := myBuildID()
	if err != nil {
		return "", err
	}
	id = strings.ReplaceAll(id, "/", ".")

	return filepath.Join(dir, "ehden-cover", id), nil
}

// return the index and values of the list of go files at the end of args.
// relies on the last flag/flag value not ending in '.go'.
// TODO I never actually use the int return
func goFiles(args []string) (int, []string) {
	for i := len(args) - 1; i >= 0; i-- {
		arg := args[i]
		if filepath.Ext(arg) != ".go" {
			return i + 1, args[i+1:]
		}
	}
	return -1, nil
}

// return the index and value of the flag, or -1 if the flag wasn't found.
// only works for flags given in the form '-flag val'.
func getFlag(args []string, flag string) (int, string) {
	flag = "-" + flag
	for i, arg := range args {
		if arg == flag && i != len(args)-1 {
			return i + 1, args[i+1]
		}
	}
	return -1, ""
}

// turns a segment of a buildID (ie just actionID) into something which can be
// used in a variable name.
func cleanIDPart(id string) string {
	return strings.ReplaceAll(id, "-", "_1")
}
