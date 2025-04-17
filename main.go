package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Config holds the application's configuration flags
type Config struct {
	QuietMode      bool
	DryRun         bool
	TrimWhitespace bool
	IgnoreCase     bool
	IgnoreBlank    bool
	ShowCounts     bool
	InputFilename  string
	OutputFilename string
	BackupSuffix   string
	DoBackup       bool
}

// Stats holds runtime statistics
type Stats struct {
	LinesRead         int
	DuplicatesFound   int
	BlankLinesSkipped int
	NewLinesOutput    int // To stdout or file
	LinesWritten      int // Specifically to file
}

// normalizeLine applies configured normalization (trimming, case)
func normalizeLine(line string, cfg *Config) string {
	if cfg.TrimWhitespace {
		line = strings.TrimSpace(line)
	}
	if cfg.IgnoreCase {
		line = strings.ToLower(line)
	}
	return line
}

// backupFile creates a backup of the source file if needed
func backupFile(filename, suffix string) error {
	if _, err := os.Stat(filename); err != nil {
		// If file doesn't exist, no need to backup
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// Other stat error
		return fmt.Errorf(
			"could not stat file for backup %q: %w",
			filename,
			err,
		)
	}

	backupName := filename + suffix
	// Simple approach: copy content. Rename could be faster but riskier on failure.
	sourceFile, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf(
			"failed to open source file for backup %q: %w",
			filename,
			err,
		)
	}
	defer sourceFile.Close()

	destFile, err := os.Create(backupName)
	if err != nil {
		return fmt.Errorf(
			"failed to create backup file %q: %w",
			backupName,
			err,
		)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return fmt.Errorf(
			"failed to copy content to backup file %q: %w",
			backupName,
			err,
		)
	}
	fmt.Fprintf(os.Stderr, "Backed up %q to %q\n", filename, backupName)
	return nil
}

func main() {
	cfg := Config{}
	stats := Stats{}

	// --- Configuration Flags ---
	flag.BoolVar(
		&cfg.QuietMode,
		"q",
		false,
		"Quiet mode (no stdout output except errors)",
	)
	flag.BoolVar(
		&cfg.DryRun,
		"d",
		false,
		"Dry run (don't write to output file)",
	)
	flag.BoolVar(
		&cfg.TrimWhitespace,
		"t",
		false,
		"Trim leading/trailing whitespace before comparison",
	)
	flag.BoolVar(&cfg.IgnoreCase, "i", false, "Ignore case during comparison")
	flag.BoolVar(&cfg.IgnoreBlank, "B", false, "Ignore blank lines from stdin")
	flag.BoolVar(
		&cfg.ShowCounts,
		"c",
		false,
		"Show counts of lines processed at the end (to stderr)",
	)
	flag.StringVar(
		&cfg.OutputFilename,
		"o",
		"",
		"Output file to append unique lines (default: use input file)",
	)
	// Backup flag needs custom handling because of optional value
	backupFlag := flag.String(
		"backup",
		"",
		"Create backup of input file (if also output file) with optional SUFFIX (default: .bak)",
	)
	flag.Usage = func() {
		fmt.Fprintf(
			os.Stderr,
			"Usage: %s [options] [input_filename]\n\n",
			os.Args[0],
		)
		fmt.Fprintf(
			os.Stderr,
			"Appends unique lines from stdin to input_filename (or -o file).\n",
		)
		fmt.Fprintf(
			os.Stderr,
			"Reads existing lines from input_filename to check for uniqueness.\n\nOptions:\n",
		)
		flag.PrintDefaults()
	}
	flag.Parse()

	// Handle backup flag presence and optional value
	if *backupFlag != "" {
		cfg.DoBackup = true
		cfg.BackupSuffix = *backupFlag
	} else {
		// Check if the flag was set without a value (e.g., --backup)
		// This is a bit hacky, relies on inspecting os.Args
		for _, arg := range os.Args[1:] {
			if arg == "--backup" || arg == "-backup" { // Check common forms
				cfg.DoBackup = true
				cfg.BackupSuffix = ".bak" // Default suffix
				break
			}
		}
	}

	if flag.NArg() > 1 {
		fmt.Fprintf(os.Stderr, "Error: Too many filename arguments.\n")
		flag.Usage()
		os.Exit(1)
	}
	cfg.InputFilename = flag.Arg(0)

	// Determine the actual target file for writing
	targetFilename := cfg.OutputFilename
	if targetFilename == "" {
		targetFilename = cfg.InputFilename // Default to writing back to input file
	}

	// --- Handle Backup ---
	// Backup the input file *only* if we intend to write back to it and backup is requested.
	if cfg.DoBackup && cfg.InputFilename != "" &&
		targetFilename == cfg.InputFilename {
		if err := backupFile(cfg.InputFilename, cfg.BackupSuffix); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating backup: %v\n", err)
			os.Exit(1)
		}
	}

	// --- Read Existing Lines (from InputFilename) ---
	existingLines := make(map[string]bool)
	if cfg.InputFilename != "" {
		file, err := os.Open(cfg.InputFilename)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				// Report errors other than "file not found"
				fmt.Fprintf(
					os.Stderr,
					"Warning: could not open input file %q for reading: %v\n",
					cfg.InputFilename,
					err,
				)
			}
			// Continue, existingLines will be empty
		} else {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				normalized := normalizeLine(scanner.Text(), &cfg)
				// Don't add empty normalized lines to the existing set if IgnoreBlank is true,
				// otherwise blank lines in the file would prevent adding blank lines from stdin.
				if normalized != "" || !cfg.IgnoreBlank {
					existingLines[normalized] = true
				}
			}
			if err := scanner.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "Error reading input file %q: %v\n", cfg.InputFilename, err)
				// Decide whether to exit or continue with a potentially incomplete set
				// os.Exit(1)
			}
		}
	}

	// --- Setup Output Writer ---
	var outputFile *os.File
	var outputWriter *bufio.Writer
	var err error

	// Only open for writing if not dryRun AND a target file is specified
	if !cfg.DryRun && targetFilename != "" {
		// Use os.O_CREATE so it works even if -o specifies a new file
		outputFile, err = os.OpenFile(
			targetFilename,
			os.O_APPEND|os.O_WRONLY|os.O_CREATE,
			0644,
		)
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"Error: failed to open output file %q for writing: %v\n",
				targetFilename,
				err,
			)
			os.Exit(1)
		}
		defer outputFile.Close()
		outputWriter = bufio.NewWriter(outputFile)
		defer outputWriter.Flush() // Ensure buffer is flushed on exit
	}

	// --- Process Stdin ---
	stdinScanner := bufio.NewScanner(os.Stdin)
	for stdinScanner.Scan() {
		stats.LinesRead++
		originalLine := stdinScanner.Text()
		normalizedLine := normalizeLine(originalLine, &cfg)

		// Handle blank lines from stdin
		if cfg.IgnoreBlank && normalizedLine == "" {
			stats.BlankLinesSkipped++
			continue
		}

		// Check for duplicates
		if existingLines[normalizedLine] {
			stats.DuplicatesFound++
			continue // Skip duplicate
		}

		// Mark as seen (handles duplicates within stdin itself)
		existingLines[normalizedLine] = true
		stats.NewLinesOutput++ // Counts lines intended for output (stdout or file)

		// Output to stdout if not quiet
		if !cfg.QuietMode {
			fmt.Println(originalLine) // Print the original line
		}

		// Append to file if writer is configured
		if outputWriter != nil { // Implies !DryRun and targetFilename != "" and OpenFile succeeded
			_, err := fmt.Fprintln(
				outputWriter,
				originalLine,
			) // Write the original line
			if err != nil {
				fmt.Fprintf(
					os.Stderr,
					"Error writing to output file %q: %v\n",
					targetFilename,
					err,
				)
				// Consider exiting or just reporting
				// os.Exit(1)
			} else {
				stats.LinesWritten++
			}
		}
	}

	if err := stdinScanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading standard input: %v\n", err)
		os.Exit(1)
	}

	// --- Report Counts ---
	if cfg.ShowCounts {
		fmt.Fprintf(os.Stderr, "--- Statistics ---\n")
		fmt.Fprintf(os.Stderr, "Lines read from stdin: %d\n", stats.LinesRead)
		if cfg.IgnoreBlank {
			fmt.Fprintf(
				os.Stderr,
				"Blank lines skipped:    %d\n",
				stats.BlankLinesSkipped,
			)
		}
		fmt.Fprintf(
			os.Stderr,
			"Duplicate lines found: %d\n",
			stats.DuplicatesFound,
		)
		if cfg.DryRun {
			fmt.Fprintf(
				os.Stderr,
				"New unique lines (dry run): %d\n",
				stats.NewLinesOutput,
			)
		} else {
			fmt.Fprintf(os.Stderr, "New unique lines output: %d\n", stats.NewLinesOutput)
			if targetFilename != "" {
				fmt.Fprintf(os.Stderr, "Lines appended to file: %d\n", stats.LinesWritten)
			}
		}
	}
}
