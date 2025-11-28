package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// We use a Map to track active uploads to prevent duplicate processing
	processingFiles   = make(map[string]bool)
	processingMutex   sync.Mutex
	successfulUploads = prometheus.NewCounter(prometheus.CounterOpts{Name: "successful_uploads", Help: "Number of successful uploads"})
	failedUploads     = prometheus.NewCounter(prometheus.CounterOpts{Name: "failed_uploads", Help: "Number of failed uploads"})
	uploadRetries     = prometheus.NewCounter(prometheus.CounterOpts{Name: "upload_retries", Help: "Number of upload retries"})
	
	paperlessBaseURL  string
	paperlessAuthToken string
	watchPath         string
	settleTime        time.Duration // Time to wait after last write
	retryDelay        time.Duration
	version           = "dev"
	whitelist         string
	verbose           bool
)

// TimerMap handles the "Debounce" logic.
// It tracks when a file was last modified and triggers a callback when it settles.
type TimerMap struct {
	sync.Mutex
	timers map[string]*time.Timer
}

func (tm *TimerMap) Reset(filePath string, duration time.Duration, onFinish func()) {
	tm.Lock()
	defer tm.Unlock()

	// If a timer already exists for this file, stop it (debounce)
	if t, ok := tm.timers[filePath]; ok {
		t.Stop()
	}

	// Create a new timer that triggers the upload
	tm.timers[filePath] = time.AfterFunc(duration, func() {
		// Clean up the map entry when the timer fires
		tm.Lock()
		delete(tm.timers, filePath)
		tm.Unlock()
		
		// Execute the callback
		onFinish()
	})
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type RealHTTPClient struct{}

func (RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

func init() {
	prometheus.MustRegister(successfulUploads, failedUploads, uploadRetries)

	// Defaults (can be overridden by Env vars)
	os.Setenv("CONSUME_FOLDER", "c:/temp")
	os.Setenv("FILE_CONSUME_WHITELIST", "*.pdf")
	os.Setenv("HTTP_UPLOAD_RETRY_DELAY_SECONDS", "5s")
	os.Setenv("FILE_STABILITY_WAIT_SECONDS", "5s") // Increased default for scanners
}

func main() {
	log.Println("Starting doc2paperless Version: " + version)
	loadConfig()

	if verbose {
		log.Println("Verbose logging enabled.")
	}

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/health/liveness", livenessHandler)

	go func() {
		log.Fatal(http.ListenAndServe(":2112", nil))
	}()

	client := RealHTTPClient{}
	
	// Create the debounce timer map
	tm := &TimerMap{timers: make(map[string]*time.Timer)}

	// Start the watcher
	watchFiles(tm, client)
}

func loadConfig() {
	var err error
	whitelist = os.Getenv("FILE_CONSUME_WHITELIST")
	paperlessBaseURL = os.Getenv("PAPERLESS_BASE_URL")
	paperlessAuthToken = os.Getenv("PAPERLESS_AUTH_TOKEN")
	watchPath = os.Getenv("CONSUME_FOLDER")
	
	if paperlessBaseURL == "" || watchPath == "" {
		log.Fatal("Missing required environment variables: PAPERLESS_BASE_URL, CONSUME_FOLDER")
	}
	if paperlessAuthToken == "" {
		log.Fatal("Environment Variable PAPERLESS_AUTH_TOKEN not set.")
	}

	// How long to wait after the LAST write event before assuming file is done
	settleTime, err = time.ParseDuration(os.Getenv("FILE_STABILITY_WAIT_SECONDS"))
	if err != nil {
		settleTime = 5 * time.Second
	}

	retryDelay, err = time.ParseDuration(os.Getenv("HTTP_UPLOAD_RETRY_DELAY_SECONDS"))
	if err != nil {
		retryDelay = 5 * time.Second
	}

	verboseStr := os.Getenv("VERBOSE")
	if verboseStr != "" {
		if v, err := strconv.ParseBool(verboseStr); err == nil {
			verbose = v
		}
	}
}

func livenessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func watchFiles(tm *TimerMap, client HTTPClient) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	err = watcher.Add(watchPath)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Watching %s for %s", watchPath, whitelist)

	// Process existing files on startup
	processExistingFiles(tm, client)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// We care about Create (new file) and Write (scanner appending data)
			if event.Op&fsnotify.Create == fsnotify.Create || event.Op&fsnotify.Write == fsnotify.Write {
				if isWhitelisted(event.Name) {
					if verbose {
						log.Println("Activity detected:", event.Name)
					}
					
					// Reset the countdown. The file will only upload if NO events 
					// happen for 'settleTime' duration.
					tm.Reset(event.Name, settleTime, func() {
						handleStableFile(event.Name, client)
					})
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("Watcher error:", err)
		}
	}
}

func processExistingFiles(tm *TimerMap, client HTTPClient) {
	files, err := os.ReadDir(watchPath)
	if err != nil {
		log.Println("Error reading directory:", err)
		return
	}
	for _, file := range files {
		if !file.IsDir() && isWhitelisted(file.Name()) {
			fullPath := filepath.Join(watchPath, file.Name())
			log.Println("Found existing file:", fullPath)
			// Treat existing files as "just modified", trigger the timer logic
			tm.Reset(fullPath, settleTime, func() {
				handleStableFile(fullPath, client)
			})
		}
	}
}

// handleStableFile is called when the timer expires (no writes for X seconds).
func handleStableFile(filePath string, client HTTPClient) {
	// 1. Concurrency Check: Ensure we aren't already uploading this file
	processingMutex.Lock()
	if processingFiles[filePath] {
		processingMutex.Unlock()
		return
	}
	processingFiles[filePath] = true
	processingMutex.Unlock()

	defer func() {
		processingMutex.Lock()
		delete(processingFiles, filePath)
		processingMutex.Unlock()
	}()

	// 2. Lock Check: Try to open the file exclusively (or just open it)
	// If the scanner is still holding the file handle, this will usually fail on Windows,
	// or return "text file busy" on Linux in some configs.
	// This is SAFER than checking file size.
	file, err := os.Open(filePath)
	if err != nil {
		if verbose {
			log.Printf("File %s is stable (time) but cannot be opened (locked?). Retrying later. Error: %v", filePath, err)
		}
		// If we can't open it, it's not ready. Reset logic would go here, 
		// but usually the scanner will trigger more Write events if it's working.
		return 
	}
	file.Close() // Close immediately, we just wanted to check access.

	// 3. Start Upload Loop
	uploadLoop(filePath, client)
}

func uploadLoop(filePath string, client HTTPClient) {
	for {
		// Final check: does file still exist?
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return
		}

		err := uploadFile(client, filePath)
		if err == nil {
			successfulUploads.Inc()
			log.Printf("Successfully uploaded: %s\n", filePath)
			
			// Remove the file
			if err := os.Remove(filePath); err != nil {
				log.Printf("Warning: uploaded %s but failed to delete: %v", filePath, err)
			}
			return
		}
		
		failedUploads.Inc()
		log.Printf("Failed to upload: %s (%v), retrying in %s...\n", filePath, err, retryDelay)
		time.Sleep(retryDelay)
	}
}

func uploadFile(client HTTPClient, filePath string) error {
	fileReader, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fileReader.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("document", filepath.Base(filePath))
	if err != nil {
		return err
	}

	_, err = io.Copy(part, fileReader)
	if err != nil {
		return err
	}

	title := filepath.Base(filePath)
	err = writer.WriteField("title", title)
	if err != nil {
		return err
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	url := strings.TrimSuffix(paperlessBaseURL, "/") + "/api/documents/post_document/"
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Token "+paperlessAuthToken)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

func isWhitelisted(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	whitelistedExtensions := strings.Split(strings.ToLower(whitelist), ",")
	for _, pattern := range whitelistedExtensions {
		if matched, _ := filepath.Match(pattern, ext); matched {
			return true
		}
	}
	return false
}
