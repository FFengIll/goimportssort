/*
goimportssort sorts your Go import lines in three categories: inbuilt, external and local.
     $ go get -u github.com/AanZee/goimportssort
*/
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"github.com/dave/dst/dstutil"
	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

var (
	list             = flag.Bool("l", false, "write results to stdout")
	write            = flag.Bool("w", false, "write result to (source) file instead of stdout")
	localPrefix      = flag.String("local", "", "put imports beginning with this string after 3rd-party packages; comma-separated list")
	verbose          bool // verbose logging
	standardPackages = make(map[string]struct{})
)

// impModel is used for storing import information
type impModel struct {
	path           string
	localReference string
}

const (
	GroupStandard int = iota // 0
	GroupThird
	GroupLocal
	GroupCount
)

type impManager struct {
	groups []*impGroup
}

type impGroup struct {
	models []*impModel
}

func (g *impGroup) append(model *impModel) {
	g.models = append(g.models, model)
}

func newImpManager() *impManager {
	groups := make([]*impGroup, GroupCount)
	for idx := range groups {
		groups[idx] = &impGroup{
			models: []*impModel{},
		}
	}
	return &impManager{groups: groups}
}

func (m *impManager) Standard() *impGroup {
	return m.groups[GroupStandard]
}

func (m *impManager) Local() *impGroup {
	return m.groups[GroupLocal]
}

func (m *impManager) ThirdPart() *impGroup {
	return m.groups[GroupThird]
}

// string is used to get a string representation of an import
func (m impModel) string() string {
	if m.localReference == "" {
		return m.path
	}

	return m.localReference + " " + m.path
}

// main is the entry point of the program
func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	err := goImportsSortMain()
	if err != nil {
		log.Fatalln(err)
	}
}

// goImportsSortMain checks passed flags and starts processing files
func goImportsSortMain() error {
	flag.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, "usage: goimportssort [flags] [path ...]\n")
		flag.PrintDefaults()
		os.Exit(2)
	}
	paths := parseFlags()

	if verbose {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	} else {
		log.SetOutput(ioutil.Discard)
	}

	if *localPrefix == "" {
		log.Println("no prefix found, using module name")

		moduleName := getModuleName()
		if moduleName != "" {
			localPrefix = &moduleName
		} else {
			log.Println("module name not found. skipping localprefix")
		}
	}

	if len(paths) == 0 {
		return errors.New("please enter a path to fix")
	}

	for _, path := range paths {
		switch dir, statErr := os.Stat(path); {
		case statErr != nil:
			return statErr
		case dir.IsDir():
			return walkDir(path)
		default:
			_, err := processFile(path, nil, os.Stdout)
			return err
		}
	}

	return nil
}

// parseFlags parses command line flags and returns the paths to process.
// It's a var so that custom implementations can replace it in other files.
var parseFlags = func() []string {
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.Parse()

	return flag.Args()
}

// isGoFile checks if the file is a go file & not a directory
func isGoFile(f os.FileInfo) bool {
	name := f.Name()
	return !f.IsDir() && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".go")
}

// walkDir walks through a path, processing all go files recursively in a directory
func walkDir(path string) error {
	return filepath.Walk(path, func(path string, f os.FileInfo, err error) error {
		if err == nil && isGoFile(f) {
			_, err = processFile(path, nil, os.Stdout)
		}
		return err
	})
}

// processFile reads a file and processes the content, then checks if they're equal.
func processFile(filename string, in io.Reader, out io.Writer) ([]byte, error) {
	log.Printf("processing %v\n", filename)

	if in == nil {
		f, err := os.Open(filename)
		if err != nil {
			return nil, err
		}
		defer closeFile(f)
		in = f
	}

	src, err := ioutil.ReadAll(in)
	if err != nil {
		return nil, err
	}

	res, err := process(src)
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(src, res) {
		// formatting has changed
		if *list {
			_, _ = fmt.Fprintln(out, string(res))
		}
		if *write {
			err = ioutil.WriteFile(filename, res, 0)
			if err != nil {
				return nil, err
			}
		}
		if !*list && !*write {
			return res, nil
		}
	} else {
		log.Println("file has not been changed")
	}

	return res, err
}

// closeFile tries to close a File and prints an error when it can't
func closeFile(file *os.File) {
	err := file.Close()
	if err != nil {
		log.Println("could not close file")
	}
}

// process processes the source of a file, categorising the imports
func process(src []byte) (output []byte, err error) {
	var (
		fileSet          = token.NewFileSet()
		convertedImports *impManager
		node             *dst.File
	)

	node, err = decorator.ParseFile(fileSet, "", src, parser.ParseComments)
	if err != nil {
		panic(err)
	}
	convertedImports, err = convertImportsToSlice(node)
	if err != nil {
		panic(err)
	}
	if convertedImports.countImports() == 0 {
		return src, err
	}

	convertedImports.sortImports()
	convertedToGo := convertedImports.convertImportsToGo()
	output, err = replaceImports(convertedToGo, node)
	if err != nil {
		panic(err)
	}

	return output, err
}

// replaceImports replaces existing imports and handles multiple import statements
func replaceImports(newImports []byte, node *dst.File) ([]byte, error) {
	var (
		output []byte
		err    error
		buf    bytes.Buffer
	)

	// remove + update
	dstutil.Apply(node, func(cr *dstutil.Cursor) bool {
		n := cr.Node()

		if decl, ok := n.(*dst.GenDecl); ok && decl.Tok == token.IMPORT {
			cr.Delete()
		}

		return true
	}, nil)

	err = decorator.Fprint(&buf, node)

	if err == nil {
		packageName := node.Name.Name
		output = bytes.Replace(buf.Bytes(), []byte("package "+packageName), append([]byte("package "+packageName+"\n\n"), newImports...), 1)
	} else {
		log.Println(err)
	}

	return output, err
}

func (m *impManager) sortImports() {
	for _, g := range m.groups {
		g.sortImports()
	}
}

// sortImports sorts multiple imports by import name & prefix
func (g *impGroup) sortImports() {
	var imports = g.models
	for x := 0; x < len(imports); x++ {
		sort.Slice(imports, func(i, j int) bool {
			if imports[i].path != imports[j].path {
				return imports[i].path < imports[j].path
			}
			return imports[i].localReference < imports[j].localReference
		})
	}
}

// convertImportsToGo generates output for correct categorised import statements
func (m *impManager) convertImportsToGo() []byte {
	output := "import ("

	for _, group := range m.groups {
		if group.countImports() == 0 {
			continue
		}
		output += "\n"
		for _, imp := range group.models {
			output += fmt.Sprintf("\t%v\n", imp.string())
		}
	}

	output += ")"

	return []byte(output)
}

func (g *impGroup) countImports() int {
	return len(g.models)
}

// countImports count the total number of imports of a [][]impModel
func (m *impManager) countImports() int {
	count := 0
	for _, group := range m.groups {
		count += group.countImports()
	}
	return count
}

// convertImportsToSlice parses the file with AST and gets all imports
func convertImportsToSlice(node *dst.File) (*impManager, error) {
	importCategories := newImpManager()

	for _, importSpec := range node.Imports {
		impName := importSpec.Path.Value
		impNameWithoutQuotes := strings.Trim(impName, "\"")
		locName := importSpec.Name

		var locImpModel impModel
		if locName != nil {
			locImpModel.localReference = locName.Name
		}
		locImpModel.path = impName

		if *localPrefix != "" && isLocalPackage(impName) {
			var group = importCategories.Local()
			group.append(&locImpModel)
		} else if isStandardPackage(impNameWithoutQuotes) {
			var group = importCategories.Standard()
			group.append(&locImpModel)
		} else {
			var group = importCategories.ThirdPart()
			group.append(&locImpModel)
		}
	}

	return importCategories, nil
}

func isLocalPackage(impName string) bool {
	// name with " or not
	if strings.HasPrefix(impName, *localPrefix) || strings.HasPrefix(impName, "\""+*localPrefix) {
		return true
	}
	return false
}

// loadStandardPackages tries to fetch all golang std packages
func loadStandardPackages() error {
	pkgs, err := packages.Load(nil, "std")
	if err == nil {
		for _, p := range pkgs {
			standardPackages[p.PkgPath] = struct{}{}
		}
	}

	return err
}

// isStandardPackage checks if a package string is included in the standardPackages map
func isStandardPackage(pkg string) bool {
	_, ok := standardPackages[pkg]
	return ok
}

// getModuleName parses the GOMOD name
func getModuleName() string {
	root, err := os.Getwd()
	if err != nil {
		log.Println("error when getting root path: ", err)
		return ""
	}

	goModBytes, err := ioutil.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		log.Println("error when reading mod file: ", err)
		return ""
	}

	modName := modfile.ModulePath(goModBytes)

	return modName
}

func init() {
	// load it in global
	err := loadStandardPackages()
	if err != nil {
		panic(err)
	}
}
