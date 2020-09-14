package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/cirruslabs/echelon/renderers"
	"github.com/fatih/color"
	"github.com/inconshreveable/log15"
	"github.com/mattn/go-colorable"
	isatty "github.com/mattn/go-isatty"
	"github.com/mitchellh/go-homedir"
	"github.com/sisatech/tablewriter"
	"github.com/sisatech/toml"
	"github.com/spf13/pflag"
	"github.com/vorteil/vorteil/pkg/elog"
	"github.com/vorteil/vorteil/pkg/vcfg"
	"github.com/vorteil/vorteil/pkg/vdisk"
	"github.com/vorteil/vorteil/pkg/vimg"
	"github.com/vorteil/vorteil/pkg/vio"
	"github.com/vorteil/vorteil/pkg/vkern"
	"github.com/vorteil/vorteil/pkg/vpkg"
	"github.com/vorteil/vorteil/pkg/vproj"
)

var renderer *renderers.InteractiveRenderer
var log elog.Logger

func main() {

	renderer = renderers.NewInteractiveRenderer(os.Stdout, nil)
	go renderer.StartDrawing()
	defer renderer.StopDrawing()
	defer func() {
		if log != nil {
			log.Finish(false)
		}
	}()

	commandInit()

	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err.Error())
		os.Exit(1)
	}
}

// log15 custom formatting
var (
	stdoutHandler log15.Handler
	stderrHandler log15.Handler
)

func init() {
	fd := os.Stdout.Fd()
	if isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd) {
		stdoutHandler = log15.StreamHandler(colorable.NewColorableStdout(), log15ColouredCLIFormat())
	} else {
		stdoutHandler = log15.StreamHandler(os.Stdout, log15CLIFormat())
	}

	fd = os.Stderr.Fd()
	if isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd) {
		stderrHandler = log15.StreamHandler(colorable.NewColorableStderr(), log15ColouredCLIFormat())
	} else {
		stderrHandler = log15.StreamHandler(os.Stderr, log15CLIFormat())
	}
}

func log15ContextFormatter(r *log15.Record) string {
	ctx := ""
	l := len(r.Ctx)
	ctxParts := make([]string, l/2, l/2+1)
	for i := 0; i < l; i += 2 {
		ctxParts[i/2] = fmt.Sprintf("%v=%v", r.Ctx[i], r.Ctx[i+1])
	}

	if l%2 != 0 {
		ctxParts = append(ctxParts, fmt.Sprintf("%v=", r.Ctx[l-1]))
	}

	if l > 0 {
		ctx = " (" + strings.Join(ctxParts, ", ") + ")"
	}
	return ctx
}

func log15ColouredCLIFormat() log15.Format {

	faint := color.New(color.Faint).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	magenta := color.New(color.FgMagenta).SprintFunc()

	colours := []func(...interface{}) string{
		magenta,
		red,
		yellow,
		func(a ...interface{}) string {
			var format string
			if len(a) > 0 {
				format = a[0].(string)
				a = a[1:]
			}
			return fmt.Sprintf(format, a...)
		},
		faint,
	}

	return log15.FormatFunc(func(r *log15.Record) []byte {
		ctx := log15ContextFormatter(r)
		x := fmt.Sprintf("%s%s\n", colours[r.Lvl](r.Msg), faint(ctx))
		return []byte(x)
	})
}

func log15CLIFormat() log15.Format {
	return log15.FormatFunc(func(r *log15.Record) []byte {
		ctx := log15ContextFormatter(r)
		x := fmt.Sprintf("%s%s\n", r.Msg, ctx)
		return []byte(x)
	})
}

func isEmptyDir(path string) bool {

	fis, err := ioutil.ReadDir(path)
	if err != nil {
		return false
	}

	if len(fis) > 0 {
		return false
	}

	return true
}

func isNotExist(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func checkValidNewDirOutput(path string, force bool, dest, flag string) error {
	if !isEmptyDir(path) && !isNotExist(path) {
		if force {
			err := os.RemoveAll(path)
			if err != nil {
				return fmt.Errorf("failed to delete existing %s '%s': %w", dest, path, err)
			}

			dir := filepath.Dir(path)
			err = os.MkdirAll(dir, 0777)
			if err != nil {
				return fmt.Errorf("failed to create parent directory for %s '%s': %w", dest, path, err)
			}
		} else {
			return fmt.Errorf("%s '%s' already exists (you can use '%s' to force an overwrite)", dest, path, flag)
		}
	}

	return nil
}

func checkValidNewFileOutput(path string, force bool, dest, flag string) error {
	if !isNotExist(path) {
		if force {
			err := os.RemoveAll(path)
			if err != nil {
				return fmt.Errorf("failed to delete existing %s '%s': %w", dest, path, err)
			}

			dir := filepath.Dir(path)
			err = os.MkdirAll(dir, 0777)
			if err != nil {
				return fmt.Errorf("failed to create parent directory for %s '%s': %w", dest, path, err)
			}
		} else {
			return fmt.Errorf("%s '%s' already exists (you can use '%s' to force an overwrite)", dest, path, flag)
		}
	}

	return nil
}

func parseImageFormat(s string) (vdisk.Format, error) {
	format, err := vdisk.ParseFormat(s)
	if err != nil {
		return format, fmt.Errorf("%w -- try one of these: %s", err, strings.Join(vdisk.AllFormatStrings(), ", "))
	}
	return format, nil
}

type vorteildConf struct {
	KernelSources struct {
		Directory          string   `toml:"directory"`
		DropPath           string   `toml:"drop-path"`
		RemoteRepositories []string `toml:"remote-repositories"`
	} `toml:"kernel-sources"`
}

var ksrc vkern.Manager

func initKernels() error {

	home, err := homedir.Dir()
	if err != nil {
		return err
	}

	vorteild := filepath.Join(home, ".vorteild")
	conf := filepath.Join(vorteild, "conf.toml")
	var kernels, watch string
	var sources []string

	confData, err := ioutil.ReadFile(conf)
	if err != nil {
		kernels = filepath.Join(vorteild, "kernels")
		watch = filepath.Join(kernels, "watch")
		sources = []string{"https://downloads.vorteil.io/system"}
	} else {
		vconf := new(vorteildConf)
		err = toml.Unmarshal(confData, vconf)
		if err != nil {
			return err
		}
		kernels = vconf.KernelSources.Directory
		watch = vconf.KernelSources.DropPath
		sources = vconf.KernelSources.RemoteRepositories
	}

	err = os.MkdirAll(kernels, 0777)
	if err != nil {
		return err
	}

	err = os.MkdirAll(watch, 0777)
	if err != nil {
		return err
	}

	ksrc, err = vkern.Advanced(vkern.AdvancedArgs{
		Directory:          kernels,
		DropPath:           watch,
		RemoteRepositories: sources,
	})
	if err != nil {
		return err
	}

	vkern.Global = ksrc
	vimg.GetKernel = ksrc.Get
	vimg.GetLatestKernel = func(ctx context.Context) (vkern.CalVer, error) {
		s, err := ksrc.Latest()
		if err != nil {
			return vkern.CalVer(""), err
		}
		k, err := vkern.Parse(s)
		if err != nil {
			return vkern.CalVer(""), err
		}
		return k, nil
	}

	return nil

}

func getPackageBuilder(argName, src string, log elog.Logger) (vpkg.Builder, error) {

	var pkgr vpkg.Reader
	var pkgb vpkg.Builder

	// check for a package file
	fi, err := os.Stat(src)
	if !os.IsNotExist(err) && (fi != nil && !fi.IsDir()) {
		f, err := os.Open(src)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
		} else {
			log.Infof("Loading package file.")

			pkgr, err = vpkg.Load(f)
			if err != nil {
				f.Close()
				return nil, err
			}

			pkgb, err = vpkg.NewBuilderFromReader(pkgr)
			if err != nil {
				pkgr.Close()
				f.Close()
				return nil, err
			}

			return pkgb, nil
		}
	}

	// check for a project directory
	var ptgt *vproj.Target
	path, target := vproj.Split(src)
	proj, err := vproj.LoadProject(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		log.Infof("Loading project directory.")

		ptgt, err = proj.Target(target)
		if err != nil {
			return nil, err
		}

		pkgb, err = ptgt.NewBuilder()
		if err != nil {
			return nil, err
		}

		return pkgb, nil
	}

	// TODO: check for urls
	// TODO: check for vrepo strings

	return nil, fmt.Errorf("failed to resolve %s '%s'", argName, src)
}

func buildImage(pkgBuilder vpkg.Builder, outputPath string, format vdisk.Format, log elog.Logger) (vio.File, *vcfg.VCFG, error) {
	err := modifyPackageBuilder(pkgBuilder)
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}

	pkgReader, err := vpkg.ReaderFromBuilder(pkgBuilder)
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}
	defer pkgReader.Close()

	pkgReader, err = vpkg.PeekVCFG(pkgReader)
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}

	cfgf := pkgReader.VCFG()
	cfg, err := vcfg.LoadFile(cfgf)
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}

	err = initKernels()
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}

	var f *os.File
	if outputPath == "" {
		f, err = ioutil.TempFile(os.TempDir(), "vorteil.disk")
		if err != nil {
			log.Errorf("error: %v", err)
			return nil, nil, err
		}
		defer f.Close()
	} else {
		f, err = os.Create(outputPath)
		if err != nil {
			log.Errorf("error: %v", err)
			return nil, nil, err
		}
		defer f.Close()
	}

	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()

	err = vdisk.Build(context.Background(), f, &vdisk.BuildArgs{
		PackageReader: pkgReader,
		Format:        format,
		KernelOptions: vdisk.KernelOptions{
			Shell: flagShell,
		},
		Logger: log,
	})
	if err != nil {
		return nil, nil, err
	}

	err = f.Close()
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}

	err = pkgReader.Close()
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}

	vf, err := vio.Open(f.Name())
	if err != nil {
		log.Errorf("error: %v", err)
		return nil, nil, err
	}

	return vf, cfg, nil
}

var (
	flagIcon             string
	flagVCFG             []string
	flagInfoDate         string
	flagInfoURL          string
	flagSystemFilesystem string
	flagSystemOutputMode string
	flagSysctl           []string
	flagVMDiskSize       string
	flagVMInodes         string
	flagVMRAM            string
	overrideVCFG         vcfg.VCFG
)

func addModifyFlags(f *pflag.FlagSet) {
	vcfgFlags.AddTo(f)
}

func modifyPackageBuilder(b vpkg.Builder) error {

	// vcfg flags
	err := vcfgFlags.Validate()
	if err != nil {
		return err
	}

	// modify
	if flagIcon != "" {
		f, err := vio.LazyOpen(flagIcon)
		if err != nil {
			return err
		}
		b.SetIcon(f)
	}

	for _, path := range flagVCFG {
		f, err := vio.Open(path)
		if err != nil {
			return err
		}

		cfg, err := vcfg.LoadFile(f)
		if err != nil {
			return err
		}

		err = b.MergeVCFG(cfg)
		if err != nil {
			return err
		}
	}

	err = b.MergeVCFG(&overrideVCFG)
	if err != nil {
		return err
	}

	return nil
}

// NumbersMode determines which numbers format a PrintableSize should render to.
var NumbersMode int

// SetNumbersMode parses s and sets NumbersMode accordingly.
func SetNumbersMode(s string) error {
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)
	switch s {
	case "", "short":
		NumbersMode = 0
	case "dec", "decimal":
		NumbersMode = 1
	case "hex", "hexadecimal":
		NumbersMode = 2
	default:
		return fmt.Errorf("numbers mode must be one of 'dec', 'hex', or 'short'")
	}
	return nil
}

// PrintableSize is a wrapper around int to alter its string formatting behaviour.
type PrintableSize int

// String returns a string representation of the PrintableSize, formatted according to the global NumbersMode.
func (c PrintableSize) String() string {
	switch NumbersMode {
	case 0:
		x := int(c)
		if x == 0 {
			return "0"
		}
		var units int
		var suffixes = []string{"", "K", "M", "G"}
		for {
			if x%1024 != 0 {
				break
			}
			x /= 1024
			units++
			if units == len(suffixes)-1 {
				break
			}
		}
		return fmt.Sprintf("%d%s", x, suffixes[units])
	case 1:
		return fmt.Sprintf("%d", int(c))
	case 2:
		return fmt.Sprintf("%#x", int(c))
	default:
		panic("invalid NumbersMode")
	}
}

// PlainTable prints data in a grid, handling alignment automatically.
func PlainTable(vals [][]string) {
	if len(vals) == 0 {
		panic(errors.New("no rows provided"))
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetBorder(false)
	table.SetColumnSeparator("")
	for i := 1; i < len(vals); i++ {
		table.Append(vals[i])
	}

	table.Render()
}
