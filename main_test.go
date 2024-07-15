package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/gotooltest"
	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"cover": main1,
	}))
}

var update = flag.Bool("u", false, "update testscript files")

func TestScript(t *testing.T) {
	p := testscript.Params{
		Dir:           "testdata",
		TestWork:      true,
		UpdateScripts: *update,
		Cmds:          map[string]func(ts *testscript.TestScript, neg bool, args []string){},
		Setup: func(env *testscript.Env) error {
			env.Setenv("HOME", filepath.Join(env.WorkDir, "home"))
			return nil
		},
	}
	if err := gotooltest.Setup(&p); err != nil {
		t.Fatal(err)
	}

	testscript.Run(t, p)
}
