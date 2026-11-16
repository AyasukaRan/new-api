package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var ErrDeepSeekFilePagesUnavailable = errors.New("PDF page counting is unavailable on this server")

// Limit untrusted native parsers independently of ordinary relay concurrency.
var deepSeekFilePageSlots = make(chan struct{}, 2)

// CountDeepSeekFilePages counts a validated PDF's page tree before it is sent
// upstream. It never uses a filename, a client-supplied count, or a guessed page.
// The caller must upload these same bytes and settle only after parsing succeeds.
func CountDeepSeekFilePages(ctx context.Context, reader io.Reader, size int64) (int, error) {
	if reader == nil || size <= 0 || size > 100<<20 {
		return 0, errors.New("file must be between 1 byte and 100 MiB")
	}
	input := bufio.NewReader(reader)
	header, err := input.Peek(5)
	if err != nil || !bytes.Equal(header, []byte("%PDF-")) {
		return 0, errors.New("file page count cannot be determined; convert the document to PDF first")
	}
	if runtime.GOOS != "linux" {
		return 0, ErrDeepSeekFilePagesUnavailable
	}
	qpdf, err := exec.LookPath("qpdf")
	if err != nil {
		return 0, ErrDeepSeekFilePagesUnavailable
	}
	prlimit, err := exec.LookPath("prlimit")
	if err != nil {
		return 0, ErrDeepSeekFilePagesUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case deepSeekFilePageSlots <- struct{}{}:
		defer func() { <-deepSeekFilePageSlots }()
	case <-ctx.Done():
		return 0, errors.New("PDF page counting timed out")
	}
	file, err := os.CreateTemp("", "new-api-pdf-*.pdf")
	if err != nil {
		return 0, ErrDeepSeekFilePagesUnavailable
	}
	defer os.Remove(file.Name())
	defer file.Close()
	// CreateTemp uses 0600. Read at most one byte beyond the advertised size so
	// a mismatched body cannot bypass either the size check or page accounting.
	written, err := io.Copy(file, io.LimitReader(input, size+1))
	if err != nil || written != size {
		return 0, errors.New("PDF file size does not match the uploaded content")
	}
	if err := file.Close(); err != nil {
		return 0, ErrDeepSeekFilePagesUnavailable
	}
	var count int
	for _, operation := range []string{"--check", "--show-npages", "--show-pages"} {
		stdout := &deepSeekPDFOutput{remaining: 1 << 20, cancel: cancel}
		stderr := &deepSeekPDFOutput{remaining: 64 << 10, cancel: cancel}
		// prlimit execs qpdf; CommandContext therefore kills the parser itself.
		// No shell, inherited credentials, external URLs, or user-selected flags.
		cmd := exec.CommandContext(ctx, prlimit, "--cpu=5", "--as=536870912", "--stack=16777216", "--fsize=0", "--core=0", "--nofile=32", "--", qpdf, operation, file.Name())
		cmd.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin"}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		cmd.WaitDelay = time.Second
		if err := cmd.Run(); err != nil || stderr.buffer.Len() != 0 {
			return 0, errors.New("PDF page count cannot be determined safely; export a valid unencrypted PDF and try again")
		}
		if operation == "--show-npages" {
			count, err = strconv.Atoi(strings.TrimSpace(stdout.buffer.String()))
			if err != nil || count < 1 || count > 1000 {
				return 0, errors.New("PDF must contain between 1 and 1000 pages")
			}
		}
		if operation == "--show-pages" {
			// qpdf 11's --show-npages also trusts /Count. --show-pages walks
			// the actual /Kids tree; require agreement so crafted metadata
			// cannot undercharge, and ambiguous damaged PDFs never reach DS.
			actual := 0
			for line := range strings.SplitSeq(stdout.buffer.String(), "\n") {
				if !strings.HasPrefix(line, "page ") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) != 5 || fields[4] != "R" {
					return 0, errors.New("PDF page tree could not be verified")
				}
				page, parseErr := strconv.Atoi(strings.TrimSuffix(fields[1], ":"))
				actual++
				if parseErr != nil || page != actual || actual > 1000 {
					return 0, errors.New("PDF page tree could not be verified")
				}
			}
			if actual != count {
				return 0, errors.New("PDF page tree and declared page count disagree; export a valid PDF and try again")
			}
		}
	}
	return count, nil
}

// Both output streams are bounded; cancellation stops a noisy parser promptly.
type deepSeekPDFOutput struct {
	buffer    bytes.Buffer
	remaining int
	cancel    context.CancelFunc
}

func (output *deepSeekPDFOutput) Write(data []byte) (int, error) {
	if len(data) > output.remaining {
		output.cancel()
		return 0, errors.New("PDF parser output exceeded the limit")
	}
	output.remaining -= len(data)
	return output.buffer.Write(data)
}
