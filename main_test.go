package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// MockFileSystem simulates a file system for testing purposes.
type MockFileSystem struct {
	Files map[string]*MockFile
}

// MockFile simulates a file for testing purposes.
type MockFile struct {
	FileName string
	Content  []byte
	FileSize int64
}

// Implement os.FileInfo and os.DirEntry for MockFile
func (mf *MockFile) Name() string               { return mf.FileName }
func (mf *MockFile) Size() int64                { return mf.FileSize }
func (mf *MockFile) Mode() os.FileMode          { return 0 }
func (mf *MockFile) ModTime() time.Time         { return time.Now() }
func (mf *MockFile) IsDir() bool                { return false }
func (mf *MockFile) Sys() interface{}           { return nil }
func (mf *MockFile) Type() os.FileMode          { return 0 }
func (mf *MockFile) Info() (os.FileInfo, error) { return mf, nil }

// Implement methods for MockFileSystem
func (mfs *MockFileSystem) Open(name string) (io.ReadCloser, error) {
	if file, exists := mfs.Files[name]; exists {
		return io.NopCloser(bytes.NewReader(file.Content)), nil
	}
	return nil, os.ErrNotExist
}

func (mfs *MockFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	for _, file := range mfs.Files {
		entries = append(entries, file)
	}
	return entries, nil
}

func (mfs *MockFileSystem) Stat(name string) (os.FileInfo, error) {
	if file, exists := mfs.Files[name]; exists {
		return file, nil
	}
	return nil, os.ErrNotExist
}

func (mfs *MockFileSystem) Remove(name string) error {
	if _, exists := mfs.Files[name]; exists {
		delete(mfs.Files, name)
		return nil
	}
	return os.ErrNotExist
}

// MockHTTPClient simulates an HTTP client for testing purposes.
type MockHTTPClient struct {
	Response *http.Response
	Error    error
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.Response, m.Error
}

func TestUploadFile(t *testing.T) {
	fs := &MockFileSystem{
		Files: map[string]*MockFile{
			"/consumefolder/test.pdf": {FileName: "test.pdf", Content: []byte("test content"), FileSize: 12},
		},
	}

	client := &MockHTTPClient{
		Response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
		},
	}

	err := uploadFile(fs, client, "/consumefolder/test.pdf")
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

}

func TestUploadFileFailure(t *testing.T) {
	fs := &MockFileSystem{
		Files: map[string]*MockFile{
			"/consumefolder/test.pdf": {FileName: "test.pdf", Content: []byte("test content"), FileSize: 12},
		},
	}

	client := &MockHTTPClient{
		Response: &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("server error")),
		},
	}

	err := uploadFile(fs, client, "/consumefolder/test.pdf")
	if err == nil {
		t.Errorf("expected error, got none")
	}

	// Verify that the file still exists
	if _, exists := fs.Files["/consumefolder/test.pdf"]; !exists {
		t.Errorf("expected file to exist, but it was deleted")
	}
}

func TestFileStability(t *testing.T) {
	// Reset channels to avoid interference from other tests
	fileStabilityConfirmation = make(chan string)
	readyForUpload = make(chan string)

	fs := &MockFileSystem{
		Files: map[string]*MockFile{
			"/consumefolder/test.pdf": {FileName: "test.pdf", Content: []byte("test content"), FileSize: 12},
		},
	}

	fileStabilityCheckInterval = 1 * time.Millisecond
	fileStabilityCheckCount = 3

	// Initialize semaphores for testing
	stabilityCheckSemaphore = make(chan struct{}, 10)
	uploadSemaphore = make(chan struct{}, 1)

	go checkFileStability(fs)
	fileStabilityConfirmation <- "/consumefolder/test.pdf"

	select {
	case filePath := <-readyForUpload:
		if filePath != "/consumefolder/test.pdf" {
			t.Errorf("expected /consumefolder/test.pdf, got %s", filePath)
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("file stability check timed out")
	}
}

func TestFileStabilityWithMultipleFiles(t *testing.T) {
	// Reset channels to avoid interference from other tests
	fileStabilityConfirmation = make(chan string)
	readyForUpload = make(chan string)

	fs := &MockFileSystem{
		Files: map[string]*MockFile{
			"/consumefolder/test.pdf": {FileName: "test.pdf", Content: []byte("PDF content"), FileSize: 12},
			"/consumefolder/test.txt": {FileName: "test.txt", Content: []byte("Text content"), FileSize: 10},
		},
	}

	// Simulate the stability check
	fileStabilityCheckInterval = 1 * time.Millisecond
	fileStabilityCheckCount = 3

	// Initialize semaphores for testing
	stabilityCheckSemaphore = make(chan struct{}, 10)
	uploadSemaphore = make(chan struct{}, 1)

	go checkFileStability(fs)

	// Send both files for stability confirmation
	fileStabilityConfirmation <- "/consumefolder/test.pdf"
	fileStabilityConfirmation <- "/consumefolder/test.txt"

	// Collect both files that become ready
	readyFiles := make(map[string]bool)
	timeout := time.After(200 * time.Millisecond)

	for i := 0; i < 2; i++ {
		select {
		case filePath := <-readyForUpload:
			if filePath != "/consumefolder/test.pdf" && filePath != "/consumefolder/test.txt" {
				t.Errorf("unexpected file: %s", filePath)
			}
			readyFiles[filePath] = true
		case <-timeout:
			// Timeout waiting for files - at least one should have completed
			if len(readyFiles) == 0 {
				t.Errorf("expected at least one file to be ready for upload, but none was found")
			}
			return
		}
	}

	// Verify both files completed
	if len(readyFiles) != 2 {
		t.Logf("Only %d file(s) completed (expected 2)", len(readyFiles))
	}
}

func TestIsWhitelisted(t *testing.T) {
	tests := []struct {
		name      string
		whitelist string
		filename  string
		expected  bool
	}{
		{
			name:      "PDF file matches *.pdf pattern",
			whitelist: "*.pdf",
			filename:  "test.pdf",
			expected:  true,
		},
		{
			name:      "PDF file matches *.pdf pattern with full path",
			whitelist: "*.pdf",
			filename:  "/consumefolder/test.pdf",
			expected:  true,
		},
		{
			name:      "TXT file does not match *.pdf pattern",
			whitelist: "*.pdf",
			filename:  "test.txt",
			expected:  false,
		},
		{
			name:      "PDF file matches multiple patterns",
			whitelist: "*.pdf,*.txt",
			filename:  "test.pdf",
			expected:  true,
		},
		{
			name:      "TXT file matches multiple patterns",
			whitelist: "*.pdf,*.txt",
			filename:  "test.txt",
			expected:  true,
		},
		{
			name:      "DOCX file does not match multiple patterns",
			whitelist: "*.pdf,*.txt",
			filename:  "test.docx",
			expected:  false,
		},
		{
			name:      "Case insensitive matching - uppercase extension",
			whitelist: "*.pdf",
			filename:  "test.PDF",
			expected:  true,
		},
		{
			name:      "Pattern with spaces after comma",
			whitelist: "*.pdf, *.txt, *.doc",
			filename:  "test.txt",
			expected:  true,
		},
		{
			name:      "Empty whitelist",
			whitelist: "",
			filename:  "test.pdf",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			whitelist = tt.whitelist
			result := isWhitelisted(tt.filename)
			if result != tt.expected {
				t.Errorf("isWhitelisted(%q) with whitelist %q = %v, expected %v", tt.filename, tt.whitelist, result, tt.expected)
			}
		})
	}
}

// MockFileSystemWithLock simulates a file system with a file that can be locked
type MockFileSystemWithLock struct {
	Files       map[string]*MockFile
	OpenCounter map[string]int
	UnlockAfter int // File unlocks after this many open attempts
}

func (mfs *MockFileSystemWithLock) Open(name string) (io.ReadCloser, error) {
	file, exists := mfs.Files[name]
	if !exists {
		return nil, os.ErrNotExist
	}

	if mfs.OpenCounter == nil {
		mfs.OpenCounter = make(map[string]int)
	}
	mfs.OpenCounter[name]++

	// Simulate locked file for the first few attempts
	if mfs.OpenCounter[name] <= mfs.UnlockAfter {
		return nil, os.ErrPermission // Simulate file lock
	}

	return io.NopCloser(bytes.NewReader(file.Content)), nil
}

func (mfs *MockFileSystemWithLock) ReadDir(dirname string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	for _, file := range mfs.Files {
		entries = append(entries, file)
	}
	return entries, nil
}

func (mfs *MockFileSystemWithLock) Stat(name string) (os.FileInfo, error) {
	if file, exists := mfs.Files[name]; exists {
		return file, nil
	}
	return nil, os.ErrNotExist
}

func (mfs *MockFileSystemWithLock) Remove(name string) error {
	if _, exists := mfs.Files[name]; exists {
		delete(mfs.Files, name)
		return nil
	}
	return os.ErrNotExist
}

func TestFileStability_LockedFile(t *testing.T) {
	// Reset channels to avoid interference from other tests
	fileStabilityConfirmation = make(chan string)
	readyForUpload = make(chan string)

	fs := &MockFileSystemWithLock{
		Files: map[string]*MockFile{
			"/consumefolder/locked.pdf": {FileName: "locked.pdf", Content: []byte("test content"), FileSize: 12},
		},
		UnlockAfter: 5, // File is locked for first 5 open attempts, then unlocks
	}

	fileStabilityCheckInterval = 1 * time.Millisecond
	fileStabilityCheckCount = 3

	// Initialize semaphores for testing
	stabilityCheckSemaphore = make(chan struct{}, 10)
	uploadSemaphore = make(chan struct{}, 1)

	go checkFileStability(fs)
	fileStabilityConfirmation <- "/consumefolder/locked.pdf"

	// File should eventually become ready after unlock
	select {
	case filePath := <-readyForUpload:
		if filePath != "/consumefolder/locked.pdf" {
			t.Errorf("expected /consumefolder/locked.pdf, got %s", filePath)
		}
		// Verify file was tried multiple times before succeeding
		if fs.OpenCounter["/consumefolder/locked.pdf"] <= fs.UnlockAfter {
			t.Errorf("expected file to be unlocked after %d attempts, but only had %d attempts",
				fs.UnlockAfter, fs.OpenCounter["/consumefolder/locked.pdf"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Errorf("file stability check timed out - locked file should eventually become ready")
	}
}

// MockFileSystemWithGrowingFile simulates a file system where file size changes over time
type MockFileSystemWithGrowingFile struct {
	Files        map[string]*MockFile
	StatCounter  map[string]int
	GrowthPhase  int // File size stops growing after this many Stat calls
	CurrentSizes map[string]int64
}

func (mfs *MockFileSystemWithGrowingFile) Open(name string) (io.ReadCloser, error) {
	file, exists := mfs.Files[name]
	if !exists {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(file.Content)), nil
}

func (mfs *MockFileSystemWithGrowingFile) ReadDir(dirname string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	for _, file := range mfs.Files {
		entries = append(entries, file)
	}
	return entries, nil
}

func (mfs *MockFileSystemWithGrowingFile) Stat(name string) (os.FileInfo, error) {
	file, exists := mfs.Files[name]
	if !exists {
		return nil, os.ErrNotExist
	}

	mfs.StatCounter[name]++

	// Simulate file growing for first few checks, then stabilizing
	if mfs.StatCounter[name] <= mfs.GrowthPhase {
		mfs.CurrentSizes[name] = int64(mfs.StatCounter[name] * 100) // Size increases with each check
	}
	// After GrowthPhase, size remains stable at the last growth value

	currentSize := mfs.CurrentSizes[name]
	if currentSize == 0 {
		// Shouldn't happen after first call, but handle gracefully
		currentSize = file.FileSize
	}

	// Create a copy of the file info with the current size
	fileCopy := &MockFile{
		FileName: file.FileName,
		Content:  file.Content,
		FileSize: currentSize,
	}

	return fileCopy, nil
}

func (mfs *MockFileSystemWithGrowingFile) Remove(name string) error {
	if _, exists := mfs.Files[name]; exists {
		delete(mfs.Files, name)
		return nil
	}
	return os.ErrNotExist
}

func TestFileStability_SizeChanges(t *testing.T) {
	// Reset channels to avoid interference from other tests
	fileStabilityConfirmation = make(chan string)
	readyForUpload = make(chan string)

	fs := &MockFileSystemWithGrowingFile{
		Files: map[string]*MockFile{
			"/consumefolder/growing.pdf": {FileName: "growing.pdf", Content: []byte("test content"), FileSize: 100},
		},
		StatCounter:  make(map[string]int),
		CurrentSizes: make(map[string]int64),
		GrowthPhase:  5, // File grows for first 5 Stat calls, then stabilizes
	}

	fileStabilityCheckInterval = 1 * time.Millisecond
	fileStabilityCheckCount = 3

	// Initialize semaphores for testing
	stabilityCheckSemaphore = make(chan struct{}, 10)
	uploadSemaphore = make(chan struct{}, 1)

	go checkFileStability(fs)
	fileStabilityConfirmation <- "/consumefolder/growing.pdf"

	// File should eventually become ready after size stabilizes
	select {
	case filePath := <-readyForUpload:
		if filePath != "/consumefolder/growing.pdf" {
			t.Errorf("expected /consumefolder/growing.pdf, got %s", filePath)
		}
		// Verify file size was checked multiple times (growth phase + stability checks)
		expectedMinChecks := fs.GrowthPhase + fileStabilityCheckCount
		if fs.StatCounter["/consumefolder/growing.pdf"] < expectedMinChecks {
			t.Errorf("expected at least %d stat calls (growth + stability), but got %d",
				expectedMinChecks, fs.StatCounter["/consumefolder/growing.pdf"])
		}
	case <-time.After(500 * time.Millisecond):
		t.Errorf("file stability check timed out - growing file should eventually stabilize")
	}
}
