package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/rjbs/go-tdf"
)

func main() {
	fontNum := flag.Int("n", 1, "font number to use (1-based)")
	list    := flag.Bool("list", false, "list fonts in the file and exit")
	between := flag.Int("b", 0, "blank columns to insert between letters")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: tdfprint [-n N] [-b N] [-list] <file.tdf> [string]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tdfprint: %v\n", err)
		os.Exit(1)
	}

	fonts, err := tdf.ParseFile(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tdfprint: %v\n", err)
		os.Exit(1)
	}
	if len(fonts) == 0 {
		fmt.Fprintf(os.Stderr, "tdfprint: no fonts found in file\n")
		os.Exit(1)
	}

	if *list {
		for i, f := range fonts {
			fmt.Printf("%d\t%s\n", i+1, f.Name)
		}
		return
	}

	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(1)
	}

	if *fontNum < 1 || *fontNum > len(fonts) {
		fmt.Fprintf(os.Stderr, "tdfprint: font %d not found (file has %d)\n", *fontNum, len(fonts))
		os.Exit(1)
	}

	fmt.Println(tdf.RenderString(fonts[*fontNum-1], flag.Arg(1), *between))
}
