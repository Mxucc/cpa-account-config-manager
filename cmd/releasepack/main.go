package main

import (
	"flag"
	"fmt"
	"os"

	"cpa-account-config-manager/internal/releasepack"
)

func main() {
	pluginID := flag.String("id", "cpa-account-config-manager", "plugin ID")
	version := flag.String("version", "", "release version without a leading v")
	goos := flag.String("goos", "", "target GOOS")
	goarch := flag.String("goarch", "", "target GOARCH")
	library := flag.String("library", "", "path to the built dynamic library")
	outputDir := flag.String("out", "dist/release", "release output directory")
	flag.Parse()

	result, errPack := releasepack.Pack(releasepack.Options{
		PluginID:  *pluginID,
		Version:   *version,
		GOOS:      *goos,
		GOARCH:    *goarch,
		Library:   *library,
		OutputDir: *outputDir,
	})
	if errPack != nil {
		fmt.Fprintln(os.Stderr, errPack)
		os.Exit(1)
	}
	fmt.Println(result.ArchivePath)
	fmt.Println(result.ChecksumPath)
}
