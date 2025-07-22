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
	fs := &MockFileSystem{
		Files: map[string]*MockFile{
			"/consumefolder/test.pdf": {FileName: "test.pdf", Content: []byte("test content"), FileSize: 12},
		},
	}

	fileStabilityCheckInterval = 1 * time.Millisecond
	fileStabilityCheckCount = 3

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
	fs := &MockFileSystem{
		Files: map[string]*MockFile{
			"/consumefolder/test.pdf": {FileName: "test.pdf", Content: []byte("PDF content"), FileSize: 12},
			"/consumefolder/test.txt": {FileName: "test.txt", Content: []byte("Text content"), FileSize: 10},
		},
	}

	// Simulate the stability check
	fileStabilityCheckInterval = 1 * time.Millisecond
	fileStabilityCheckCount = 3

	go checkFileStability(fs)

	// Send both files for stability confirmation
	fileStabilityConfirmation <- "/consumefolder/test.pdf"
	fileStabilityConfirmation <- "/consumefolder/test.txt"

	// Check the result in the readyForUpload channel
	select {
	case filePath := <-readyForUpload:
		if filePath != "/consumefolder/test.pdf" {
			t.Errorf("expected /consumefolder/test.pdf, got %s", filePath)
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("expected a file to be ready for upload, but none was found")
	}

	// Ensure no other files are pushed to the channel
	select {
	case filePath := <-readyForUpload:
		t.Errorf("unexpected file pushed to channel: %s", filePath)
	case <-time.After(10 * time.Millisecond):
		// No additional files should be pushed
	}
}
