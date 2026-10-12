package semantic

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const modSrc = `package users

import "fmt"

type User struct{ ID string }

func FetchUser(id string) *User { return &User{ID: id} }

var DefaultLimit = 10

const Version = "1"

func helper() { fmt.Println("x") } // unexported, not an export

func (u *User) String() string { return u.ID } // method, not a package-level export
`

func TestGoAnalyzerExports(t *testing.T) {
	es, err := GoAnalyzer{}.Exports(modSrc)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DefaultLimit", "FetchUser", "User", "Version"}
	if !reflect.DeepEqual(es.Names, want) {
		t.Fatalf("exports = %v, want %v (sorted)", es.Names, want)
	}
	if es.Has("helper") || es.Has("String") {
		t.Error("unexported func / method must not be exports")
	}
}

const callerSrc = `package app

import (
	"fmt"
	u "example.com/x/users"
)

func Run() {
	fmt.Println(u.FetchUser("a").ID)
	_ = u.DefaultLimit
}
`

func TestGoAnalyzerImports(t *testing.T) {
	refs, err := GoAnalyzer{}.Imports(callerSrc)
	if err != nil {
		t.Fatal(err)
	}
	// Expect two imports; find the aliased users one.
	var users *ImportRef
	for i := range refs {
		if refs[i].Path == "example.com/x/users" {
			users = &refs[i]
		}
	}
	if users == nil {
		t.Fatal("users import not found")
	}
	if users.Name != "u" {
		t.Errorf("alias name = %q, want u", users.Name)
	}
	if !reflect.DeepEqual(users.Selectors, []string{"DefaultLimit", "FetchUser"}) {
		t.Errorf("selectors = %v, want [DefaultLimit FetchUser] (sorted, exported only)", users.Selectors)
	}
}

func TestSemanticConflictsParsesBuildDiag(t *testing.T) {
	diag := "# example.com/difftest\n" +
		"./caller.go:5:34: undefined: users.GetUser\n" +
		"./caller2.go:3:29: undefined: LocalMissing\n"
	refs, err := SemanticConflicts(".", diag)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 broken refs, got %d: %+v", len(refs), refs)
	}
	// Sorted by file: caller.go (missing-export) before caller2.go (missing-symbol).
	if refs[0].FromFile != "caller.go" || refs[0].Pkg != "users" || refs[0].Name != "GetUser" || refs[0].Kind != "missing-export" {
		t.Errorf("ref0 = %+v", refs[0])
	}
	if refs[1].FromFile != "caller2.go" || refs[1].Pkg != "" || refs[1].Name != "LocalMissing" || refs[1].Kind != "missing-symbol" {
		t.Errorf("ref1 = %+v", refs[1])
	}
}

func TestInferRenameUnambiguous(t *testing.T) {
	base := "package users\nfunc GetUser(id string) string { return id }\n"
	now := "package users\nfunc FetchUser(id string) string { return id }\n"
	name, ok := InferRename(base, now, "GetUser")
	if !ok || name != "FetchUser" {
		t.Fatalf("InferRename = (%q, %v), want (FetchUser, true)", name, ok)
	}
}

func TestInferRenameRefusesWhenAmbiguous(t *testing.T) {
	// Two symbols added: cannot attribute the rename safely.
	base := "package users\nfunc GetUser(id string) string { return id }\n"
	now := "package users\nfunc FetchUser(id string) string { return id }\nfunc LookupUser(id string) string { return id }\n"
	if _, ok := InferRename(base, now, "GetUser"); ok {
		t.Error("ambiguous add set must not infer a rename")
	}
}

func TestInferRenameRefusesPureDeletion(t *testing.T) {
	base := "package users\nfunc GetUser(id string) string { return id }\nfunc KeepMe() {}\n"
	now := "package users\nfunc KeepMe() {}\n"
	if _, ok := InferRename(base, now, "GetUser"); ok {
		t.Error("a genuine deletion must NOT be papered over as a rename")
	}
}

func TestRepairRenameRewritesSelectors(t *testing.T) {
	dir := t.TempDir()
	// Two files import a package named "users"; one references the renamed symbol.
	writeFile(t, dir, "caller.go", "package app\n\nimport \"example.com/x/users\"\n\nfunc Run() string { return users.GetUser(\"a\") }\n")
	// A decoy: a local var also named users must NOT be rewritten (no matching import).
	writeFile(t, dir, "decoy.go", "package app\n\nfunc Decoy() {\n\tusers := struct{ GetUser func(string) string }{}\n\t_ = users.GetUser(\"b\")\n}\n")

	n, err := RepairRename(dir, "users", "GetUser", "FetchUser")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("changed %d files, want 1 (only the importing file)", n)
	}
	caller := readFile(t, dir, "caller.go")
	if !strings.Contains(caller, "users.FetchUser(\"a\")") {
		t.Errorf("selector not rewritten:\n%s", caller)
	}
	if strings.Contains(caller, "GetUser") {
		t.Errorf("old selector survived:\n%s", caller)
	}
	decoy := readFile(t, dir, "decoy.go")
	if !strings.Contains(decoy, "users.GetUser(\"b\")") {
		t.Errorf("decoy (no matching import) must be untouched:\n%s", decoy)
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
