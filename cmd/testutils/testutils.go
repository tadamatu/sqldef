// Utilities for _test.go files
package testutils

import (
	"fmt"
	"io/ioutil"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k0kubun/sqldef/database"
	"github.com/k0kubun/sqldef/schema"
	"gopkg.in/yaml.v2"
)

type TestCase struct {
	Current    string  // default: empty schema
	Desired    string  // default: empty schema
	Output     *string // default: use Desired as Output
	MinVersion string  `yaml:"min_version"`
	MaxVersion string  `yaml:"max_version"`
}

func ReadTests(pattern string) (map[string]TestCase, error) {
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	ret := map[string]TestCase{}
	for _, file := range files {
		var tests map[string]*TestCase

		buf, err := ioutil.ReadFile(file)
		if err != nil {
			return nil, err
		}

		err = yaml.UnmarshalStrict(buf, &tests)
		if err != nil {
			return nil, err
		}

		for name, test := range tests {
			if test.Output == nil {
				test.Output = &test.Desired
			}
			if _, ok := ret[name]; ok {
				log.Fatal(fmt.Sprintf("There are multiple test cases named '%s'", name))
			}
			ret[name] = *test
		}
	}

	return ret, nil
}

func RunTest(t *testing.T, db database.Database, test TestCase, mode schema.GeneratorMode, version string) {
	if test.MinVersion != "" && compareVersion(t, version, test.MinVersion) < 0 {
		t.Skipf("Version '%s' is smaller than min_version '%s'", version, test.MaxVersion)
	}
	if test.MaxVersion != "" && compareVersion(t, version, test.MaxVersion) > 0 {
		t.Skipf("Version '%s' is larger than max_version '%s'", version, test.MaxVersion)
	}

	// Prepare current
	if test.Current != "" {
		ddls, err := splitDDLs(mode, test.Current)
		if err != nil {
			t.Fatal(err)
		}
		err = runDDLs(db, ddls)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test idempotency
	dumpDDLs, err := db.DumpDDLs()
	if err != nil {
		log.Fatal(err)
	}
	ddls, err := schema.GenerateIdempotentDDLs(mode, test.Current, dumpDDLs, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ddls) > 0 {
		t.Errorf("expected nothing is modifed, but got:\n```\n%s```", joinDDLs(ddls))
	}

	// Main test
	dumpDDLs, err = db.DumpDDLs()
	if err != nil {
		log.Fatal(err)
	}
	ddls, err = schema.GenerateIdempotentDDLs(mode, test.Desired, dumpDDLs, []string{})
	if err != nil {
		t.Fatal(err)
	}
	expected := *test.Output
	actual := joinDDLs(ddls)
	if expected != actual {
		t.Errorf("\nexpected:\n```\n%s```\n\nactual:\n```\n%s```", expected, actual)
	}
	err = runDDLs(db, ddls)
	if err != nil {
		t.Fatal(err)
	}

	// Test idempotency
	dumpDDLs, err = db.DumpDDLs()
	if err != nil {
		log.Fatal(err)
	}
	ddls, err = schema.GenerateIdempotentDDLs(mode, test.Desired, dumpDDLs, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ddls) > 0 {
		t.Errorf("expected nothing is modifed, but got:\n```\n%s```", joinDDLs(ddls))
	}
}

// left < right: compareVersion() < 0
// left = right: compareVersion() = 0
// left > right: compareVersion() > 0
func compareVersion(t *testing.T, leftVersion string, rightVersion string) int {
	leftVersions := strings.Split(leftVersion, ".")
	rightVersions := strings.Split(rightVersion, ".")

	// Compare only specified segments
	length := len(leftVersions)
	if length > len(rightVersions) {
		length = len(rightVersions)
	}

	for i := 0; i < length; i++ {
		left, err := strconv.Atoi(leftVersions[i])
		if err != nil {
			t.Fatal(err)
		}
		right, err := strconv.Atoi(rightVersions[i])
		if err != nil {
			t.Fatal(err)
		}

		if left < right {
			return -1
		} else if left > right {
			return 1
		}
	}
	return 0
}

func splitDDLs(mode schema.GeneratorMode, str string) ([]string, error) {
	statements, err := schema.ParseDDLs(mode, str)
	if err != nil {
		return nil, err
	}

	var ddls []string
	for _, statement := range statements {
		ddls = append(ddls, statement.Statement())
	}
	return ddls, nil
}

func runDDLs(db database.Database, ddls []string) error {
	transaction, err := db.DB().Begin()
	if err != nil {
		return err
	}
	for _, ddl := range ddls {
		var err error
		if database.TransactionSupported(ddl) {
			_, err = transaction.Exec(ddl)
		} else {
			_, err = db.DB().Exec(ddl)
		}
		if err != nil {
			rollbackErr := transaction.Rollback()
			if rollbackErr != nil {
				return rollbackErr
			}
			return err
		}
	}
	return transaction.Commit()
}

func joinDDLs(ddls []string) string {
	var builder strings.Builder
	for _, ddl := range ddls {
		builder.WriteString(ddl)
		builder.WriteString(";\n")
	}
	return builder.String()
}

func MustExecute(command string, args ...string) string {
	out, err := execute(command, args...)
	if err != nil {
		log.Printf("failed to execute '%s %s': `%s`", command, strings.Join(args, " "), out)
		log.Fatal(err)
	}
	return out
}

func execute(command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
