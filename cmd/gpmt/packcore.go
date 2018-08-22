package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pivotal-gss/gpmt/utils"
)

var (
	//packcore_flags *flag.FlagSet
	cmd            *string
	core           *string
	binary         *string
	use_gdb        *bool
	keep_tmp_dir   *bool
	ignore_missing *bool
	disablPrompt   *bool

	// Storage for old environment variables
	backupLDPath     string
	backupPythonHome string
	backupPythonPath string
	backupPath       string

	// Commands used for collection
	lddCmd = "ldd"
	gdbCmd = "gdb"
)

func copyFilePath(src string, dst string) error {
	log.Debugf("Deep copying '%s' to '%s'\n", src, dst)
	srcDir := filepath.Dir(src)
	if strings.Index(srcDir, "/") == -1 {
		srcDir = srcDir[1:]
	}
	dstDir := filepath.Join(dst, srcDir)
	if _, err := os.Stat(dstDir); os.IsNotExist(err) {
		os.MkdirAll(dstDir, 0755)
	}
	return copyFile(strings.Trim(src, "\n "), dstDir)
}

func copyFile(filename string, dest string) error {
	log.Debugf("Copying %s to %s\n", filename, dest)
	if err := exec.Command("cp", filename, dest).Run(); err != nil {
		return errors.New(fmt.Sprintf("Failed copying file '%s': %s\n", filename, err))
	}
	return nil
}

func copyFiles(filenames []string, dest string, deep bool) error {
	for _, file := range filenames {
		if deep {
			err := copyFilePath(file, dest)
			if err != nil {
				return err
			}
		} else {
			err := copyFile(file, dest)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	LSB_RELEASE = "lsb_release"
)

func writePlatformInfo(dest string) error {
	lsb_release_binary, err := exec.LookPath(LSB_RELEASE)
	if err != nil {
		matches, _ := filepath.Glob("/etc/*release")
		copyFiles(matches, dest, true)
	} else {
		cmd := exec.Command(lsb_release_binary, "-a", ">"+dest+"/lsb_release.out")
		if err := cmd.Run(); err != nil {
			log.Infof("Failed executing command: %s\n", cmd.Args)
		}
	}

	if _, err := os.Stat("/etc/gpdb-appliance-version"); err == nil {
		copyFile("/etc/gpdb-appliance-version", dest)
	}

	cmd := exec.Command("uname", "-r")
	uname_output, err := cmd.Output()
	if err != nil {
		log.Infof("Failed executing command: %s\n", cmd.Args)
	}
	uname_outfile := dest + "/" + "uname.out"
	f, err := os.Create(uname_outfile)
	if err != nil {
		log.Infof("Could not create file '%s': %s\n", uname_outfile, err)
	}
	f.WriteString(string(uname_output))
	f.Close()

	return nil
}

func getLibraryListWithLDD(binary string) ([]byte, []string, error) {

	static_libraries := []string{
		"/lib64/libgcc_s.so.1",
		"/lib64/libnss_files.so.2",
		"/lib/libgcc_s.so.1",
		"/lib/libnss_files.so.2",
	}

	libraries := []string{}

	for _, lib := range static_libraries {
		if _, err := os.Stat(lib); err == nil {
			libraries = append(libraries, lib)
		}
	}

	log.Infoln("Running ldd on %s", binary)
	cmd := exec.Command(lddCmd, binary)
	ldd_output, err := cmd.Output()
	if err != nil {
		log.Infof("Error executing %s: %s\n", cmd.Args, err)
		return ldd_output, []string{}, err
	}

	log.Debugf("ldd_output\n%s\n", string(ldd_output))
	log.Infoln("Verifying ldd output")
	re := regexp.MustCompile("=> (\\S+)\\s+")
	missingLibraries := []string{}
	for _, line := range strings.Split(string(ldd_output), "\n") {
		match := strings.TrimSpace(re.FindString(line))
		if match == "" {
			continue
		}
		match = match[3:]
		log.Debugf("Library Location parsed: '%s'\n", match)
		switch match {
		case "not":
			index := strings.Index(line, " =>")
			missingLibraries = append(missingLibraries, strings.TrimSpace(line[:index]))
		case "":
		default:
			libraries = append(libraries, match)
		}
	}
	if len(missingLibraries) > 0 {
		errString := "Unable to find libraries: \n"
		for _, lib := range missingLibraries {
			errString = errString + fmt.Sprintf("    %s\n", lib)
		}
		if *ignore_missing {
			log.Warnln(errString)
		} else {
			return nil, nil, errors.New(fmt.Sprintf("%s\nPlease check environment or run with '-ignore_missing' flag\n", errString))
		}
	}
	return ldd_output, libraries, nil
}

func getLibraryListWithGDB(coreFile string, binary string) ([]byte, []string, error) {

	libraries := []string{}

	log.Infoln("Running gdb on core %s with %s", coreFile, binary)
	cmd := exec.Command(gdbCmd, "--batch", "--ex", "info sharedlibrary", binary, coreFile)
	gdb_output, err := cmd.Output()
	if err != nil {
		log.Infof("Error executing %s: %s\n", cmd.Args, err)
		return gdb_output, []string{}, err
	}

	log.Debugf("gdb_output\n%s\n", string(gdb_output))
	log.Infoln("Verifying gdb output")
	re := regexp.MustCompile("^0x[0-9a-f]+.*\\s(/.*$)")
	missingLibraries := []string{}
	for _, line := range strings.Split(string(gdb_output), "\n") {
		match := re.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		log.Debugf("Library Location parsed: '%s'\n", match[1])
		if _, err := os.Stat(match[1]); err == nil {
			libraries = append(libraries, match[1])
		} else {
			missingLibraries = append(missingLibraries, match[1])
		}

	}
	if len(missingLibraries) > 0 {
		errString := "Unable to find libraries: \n"
		for _, lib := range missingLibraries {
			errString = errString + fmt.Sprintf("    %s\n", lib)
		}
		if *ignore_missing {
			log.Warnln(errString)
		} else {
			return nil, nil, errors.New(fmt.Sprintf("%s\nPlease check environment or run with '-ignore_missing' flag\n", errString))
		}
	}
	return gdb_output, libraries, nil

}

var GDB_SCRIPT_HEADER = [...]string{
	"#!/bin/bash",
	"unset PYTHONHOME",
	"unset PYTHONPATH",
	"curDIR=`pwd`",
}

func generateGDBScript(dir string, binary string, core string) error {
	filename := dir + "/runGDB.sh"
	f, err := os.Create(filename)
	if err != nil {
		return errors.New(fmt.Sprintf("Could not create file '%s': %s\n", filename, err))
	}
	for _, s := range GDB_SCRIPT_HEADER {
		f.WriteString(s + "\n")
	}
	f.WriteString(fmt.Sprintf("/usr/bin/gdb --eval-command=\"set sysroot $curDIR\" --eval-command=\"core %s\" %s\n", core, binary))
	err = os.Chmod(filename, 0755)
	if err != nil {
		return errors.New(fmt.Sprintf("Could not chmod file '%s': %s\n", filename, err))
	}

	return nil
}

func checkPackageContents(packdir string, coreFile string, binary string, libraries []string) error {
	missingLibraries := []string{}
	if _, err := os.Stat(packdir); os.IsNotExist(err) {
		return errors.New(fmt.Sprintf("Missing packcore directory: %s\n", packdir))
	}
	if coreFile != "" {
		if _, err := os.Stat(filepath.Join(packdir, filepath.Base(coreFile))); os.IsNotExist(err) {
			return errors.New(fmt.Sprintf("Failed to copy corefile '%s'\n", coreFile))
		}
	}
	if _, err := os.Stat(filepath.Join(packdir, filepath.Base(binary))); os.IsNotExist(err) {
		return errors.New(fmt.Sprintf("Failed to copy binrary '%s'\n", binary))
	}
	for _, lib := range libraries {
		if _, err := os.Stat(filepath.Join(packdir, strings.TrimSpace(lib))); os.IsNotExist(err) {
			missingLibraries = append(missingLibraries, lib)
		}
	}
	if len(missingLibraries) > 0 {
		errString := ""
		for _, lib := range missingLibraries {
			errString = errString + fmt.Sprintf("\t%s\n", lib)
		}
		return errors.New("Missing Libraries!\n" + errString)
	}
	if _, err := os.Stat(filepath.Join(packdir, "ldd_output")); os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(packdir, "gdb_output")); os.IsNotExist(err) {
			return errors.New("Missing 'gdb_output' or 'ldd_output'")
		}
	}
	if _, err := os.Stat(filepath.Join(packdir, "runGDB.sh")); os.IsNotExist(err) {
		return errors.New("Failed to create 'runGDB.sh'")
	}

	return nil
}

func cleanupWorkDir(packDir string) {
	log.Debugf("Cleaning work dir\n")
	if !*keep_tmp_dir {
		log.Debugf("Removing temp directory %s\n", packDir)
		exec.Command("rm", "-rf", packDir, ".").Run()
	}
}

func setGDBEnv() error {
	backupLDPath = os.Getenv("LD_LIBRARY_PATH")
	oserr := os.Unsetenv("LD_LIBRARY_PATH")
	if oserr != nil {
		return oserr
	}
	backupPythonHome = os.Getenv("PYTHONHOME")
	oserr = os.Unsetenv("PYTHONHOME")
	if oserr != nil {
		return oserr
	}
	backupPythonPath = os.Getenv("PYTHONPATH")
	oserr = os.Unsetenv("PYTHONPATH")
	if oserr != nil {
		return oserr
	}
	backupPath = os.Getenv("PATH")
	oserr = os.Setenv("PATH", fmt.Sprintf("/usr/bin:%s", backupPath))
	if oserr != nil {
		return oserr
	}
	return nil
}

func unsetGDBEnv() error {
	// restore OS Envs
	oserr := os.Setenv("LD_LIBRARY_PATH", backupLDPath)
	if oserr != nil {
		return oserr
	}
	oserr = os.Setenv("PYTHONHOME", backupPythonHome)
	if oserr != nil {
		return oserr
	}
	oserr = os.Setenv("PYTHONPATH", backupPythonPath)
	if oserr != nil {
		return oserr
	}
	oserr = os.Setenv("PATH", backupPath)
	if oserr != nil {
		return oserr
	}
	return nil
}

func packCoreFile(coreFile string, binary string) error {

	packDir := "./packcore-" + path.Base(coreFile)

	defer cleanupWorkDir(packDir)

	log.Infof("Creating temp directory %s\n", packDir)
	// Check if directory already exists (if user reruns with same core file)
	if _, err := os.Stat(packDir); err == nil {
		confirm := "n"
		fmt.Printf("Temp directory '%s' already exists... Delete it? Yy|Nn (default=N): ", packDir)
		fmt.Scanf("%s", &confirm)
		confirm = strings.ToLower(confirm)
		if confirm != "y" && confirm != "yes" {
			os.Exit(0)
		}

		cleanupWorkDir(packDir)
	}

	if err := os.Mkdir(packDir, 0755); err != nil {
		return errors.New(fmt.Sprintf("Not able to create directory '%s'\n", packDir))
	}

	var err error = nil
	var libraries []string

	var usegdb = true
	var useldd = true

	err = utils.SetupBinaries(&gdbCmd)
	if err != nil {
		log.Debugln("Error finding gdb: %s", err)
		usegdb = false
	}

	err = utils.SetupBinaries(&lddCmd)
	if err != nil {
		log.Debugln("Error finding ldd: %s", err)
		useldd = false
	}

	switch {
	case usegdb:
		var gdb_output []byte

		// Disable environment variables that might cause issues with gdb
		err = setGDBEnv()
		if err != nil {
			return err
		}

		gdb_output, libraries, err = getLibraryListWithGDB(coreFile, binary)
		if err != nil {
			unsetGDBEnv()
			return err
		}

		log.Infof("Writing gdb output\n")
		err = ioutil.WriteFile(path.Join(packDir, "gdb_output"), gdb_output, 0644)
		if err != nil {
			return errors.New(fmt.Sprintf("Failed to write file %v (GDB command output)\n", path.Join(packDir, "gcc_output")))
		}

		err = unsetGDBEnv()
		if err != nil {
			return err
		}

	case useldd:
		var ldd_output []byte

		ldd_output, libraries, err = getLibraryListWithLDD(binary)
		if err != nil {
			return errors.New(fmt.Sprintf("%s", err))
		}

		log.Infof("Writing ldd output\n")
		err = ioutil.WriteFile(path.Join(packDir, "ldd_output"), ldd_output, 0644)
		if err != nil {
			return errors.New(fmt.Sprintf("Failed to write to file %v (LDD command output)\n", path.Join(packDir, "ldd_output")))
		}

	default:
		return errors.New("Could not find gdb or ldd. Cannot collect artifacts")
	}

	log.Infoln("Writing platform info")
	writePlatformInfo(packDir)

	log.Infoln("Copying libraries")
	copyFiles(libraries, packDir, true)

	log.Infof("Copying core %s\n", coreFile)
	copyFile(coreFile, packDir)

	log.Infof("Copying binary %s\n", binary)
	copyFile(binary, packDir)

	log.Infoln("Generating gdb script")
	err = generateGDBScript(packDir, path.Base(binary), path.Base(coreFile))
	if err != nil {
		return err
	}

	log.Infoln("Checking collected files")
	err = checkPackageContents(packDir, coreFile, binary, libraries)
	if err != nil {
		return err
	}

	cmd := exec.Command("tar", "czf", "packcore-"+path.Base(coreFile)+".tar.gz", packDir)
	log.Infof("Creating tar bundle %s\n", cmd.Args)
	if err = cmd.Run(); err != nil {
		return errors.New(fmt.Sprintf("Failed executing %s: %s\n", cmd.Args, err))
	}

	log.Infof("Packcore generated:\n\t%s\n", cmd.Args[2])

	return nil
}

func getFileInfo(filename string) (string, error) {
	cmd := exec.Command("/usr/bin/file", filename)
	fileOutput, err := cmd.Output()
	if err != nil {
		return "", errors.New(fmt.Sprintf("Failed executing %s: %s\n", cmd.Args, err))
	}
	return string(fileOutput), nil
}

func isCore(fileOutput string) bool {
	if strings.Index(fileOutput, "LSB core file") == -1 {
		return false
	}
	return true
}

func findBinary(fileCmdOutput string) (string, error) {
	start := strings.Index(fileCmdOutput, "'") + 1
	end := strings.Index(fileCmdOutput[start:], "'") + start
	cmd := strings.Split(fileCmdOutput[start:end], " ")[0]
	cmd = strings.Trim(cmd, "\n ")

	if strings.HasSuffix(cmd, ":") {
		cmd = cmd[:len(cmd)-1]
	}

	log.Debugf("Parsing binary name from file output: %s\n", cmd)
	ret, err := exec.LookPath(cmd)
	if err != nil {
		return "", errors.New(fmt.Sprintf("Unable to find binary name %s\n", cmd))
	}

	return ret, nil
}

func collect_core() error {

	// Verify if core value was specified
	if *core == "" {
		return errors.New("No core file specified")
	}

	// Verify core file exists
	if _, err := os.Stat(*core); os.IsNotExist(err) {
		return errors.New(fmt.Sprintf("Corefile '%s' does not exist", *core))
	}

	coreFile, err := filepath.Abs(*core)
	if err != nil {
		return errors.New(fmt.Sprintf("Could not determine absolute path for %s: %s", *core, err))
	}
	log.Debugf("coreFile full path: %s\n", coreFile)

	fileInfo, err := getFileInfo(coreFile)
	if err != nil {
		log.Errorf("%s", err)
	}

	fileCmdOutput := strings.Trim(fileInfo, "\n ")
	log.Debugf("'file' command returned: %s\n", fileCmdOutput)

	if !isCore(fileCmdOutput) {
		return errors.New("File does not appear to be a core")
	}
	log.Debugf("Confirmed %s looks like a core file\n", coreFile)
	if *binary != "" {
		*binary, err = exec.LookPath(*binary)
		if err != nil {
			return errors.New("Unable to find binary specified on command line")
		}
	} else {
		*binary, err = findBinary(fileCmdOutput)
		if err != nil {
			log.Errorf("%s", err)
		}
	}
	log.Debugf("binary full path %s\n", *binary)
	err = packCoreFile(coreFile, *binary)
	if err != nil {
		return err
	}
	return nil
}

// Command constants
const (
	CMD_PACKCORE_COLLECT = "collect"
)

// Command -> function map
var (
	func_map = map[string]interface{}{
		CMD_PACKCORE_COLLECT: collect_core,
	}
)

/*
	Prompts user to continue and returns true for continue or false for stop
*/
func promptWarning() bool {
	if *disablPrompt {
		return true
	}
	fmt.Printf("\nContinue executing packcore? Yy|Nn (default=Y): ")
	confirm := "n"
	fmt.Scanf("%s", &confirm)
	confirm = strings.ToLower(confirm)
	if confirm != "y" && confirm != "yes" {
		return false
	}
	return true
}

func Args(c *context.Context) {
	c.Logger.Debugf("Flags: %s\n", c.Flags)
	cmd = c.Flags.String("cmd", "", "")
	core = c.Flags.String("core", "", "")
	binary = c.Flags.String("binary", "", "")
	disablPrompt = c.Flags.Bool("a", false, "")
	use_gdb = c.Flags.Bool("use_gdb", false, "")
	keep_tmp_dir = c.Flags.Bool("keep_tmp_dir", false, "")
	ignore_missing = c.Flags.Bool("ignore_missing", false, "")
}

func RunTool(c context.Context) error {
	// Initialize flagsets
	/*packcore_flags = flag.NewFlagSet("packcore_flags", flag.ContinueOnError)
	cmd = packcore_flags.String("cmd", "", "Show database ages")
	core = packcore_flags.String("core", "", "")
	binary = packcore_flags.String("binary", "", "")
	use_gdb = packcore_flags.Bool("use_gdb", false, "")
	help = packcore_flags.Bool("help", false, "")
	verbose = packcore_flags.Bool("verbose", false, "")
	keep_tmp_dir = packcore_flags.Bool("keep_tmp_dir", false, "")*/

	log = c.Logger

	if *cmd == "" {
		Print_help_packcore()
		return nil
	}

	log.Debugf("Using options core=%s, binary=%s, use_gdb=%v, keep_tmp_dir=%v, ignore_missing=%v\n", *core, *binary, *use_gdb, *keep_tmp_dir, *ignore_missing)

	// warn user if GPHOME is not set
	continuePackcore := true
	err := utils.CheckGPHOME()
	if err != nil {
		log.Warnf("Detected problem with $GPHOME environmental variable\n%s\n", err)
		continuePackcore = promptWarning()
	}
	if !continuePackcore {
		return errors.New("Canceling packcore due to user request")
	}

	v := func_map[*cmd]
	if v != nil {
		return v.(func() error)() // return error from func
	} else {
		return errors.New(fmt.Sprintf("Unrecognized command '%s'", *cmd))
		log.Fatalf("Unrecognized command '%s'\n", *cmd)
	}
	return nil
}
