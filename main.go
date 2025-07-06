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
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	readyForUpload             = make(chan string)
	fileStabilityConfirmation  = make(chan string)
	successfulUploads          = prometheus.NewCounter(prometheus.CounterOpts{Name: "successful_uploads", Help: "Number of successful uploads"})
	failedUploads              = prometheus.NewCounter(prometheus.CounterOpts{Name: "failed_uploads", Help: "Number of failed uploads"})
	uploadRetries              = prometheus.NewCounter(prometheus.CounterOpts{Name: "upload_retries", Help: "Number of upload retries"})
	paperlessBaseURL           string
	paperlessAuthToken         string
	watchPath                  string
	fileStabilityCheckInterval time.Duration
	fileStabilityCheckCount    int
	retryDelay                 time.Duration
)

func init() {
	prometheus.MustRegister(successfulUploads, failedUploads, uploadRetries)

	os.Setenv("WATCH_PATH", "/consumefolder")
	os.Setenv("PAPERLESS_BASE_URL", "http://localhost:8000")
	os.Setenv("RETRY_DELAY", "5s")
	os.Setenv("STABILITY_CHECKS", "3")
	os.Setenv("CHECK_INTERVAL", "1s")
	os.Setenv("PAPERLESS_AUTH_TOKEN", "281298728b981fb7c86d14a77f85e686974e6c4c")
}

func main() {
	// Load configuration from environment variables
	loadConfig()

	// Start Prometheus metrics server
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		log.Fatal(http.ListenAndServe(":2112", nil))
	}()

	// Start file watcher
	go watchFiles()

	// Start file stability checker
	go checkFileStability()

	// Start file uploader
	uploadFiles()

	select {} // Block forever
}

func loadConfig() {
	var err error
	paperlessBaseURL = os.Getenv("PAPERLESS_BASE_URL")
	paperlessAuthToken = os.Getenv("PAPERLESS_AUTH_TOKEN")
	watchPath = os.Getenv("WATCH_PATH")
	if paperlessBaseURL == "" || watchPath == "" {
		log.Fatal("Missing required environment variables: PAPERLESS_BASE_URL, WATCH_PATH")
	}

	fileStabilityCheckInterval, err = time.ParseDuration(os.Getenv("FILE_STABILITY_CHECK_INTERVAL"))
	if err != nil {
		fileStabilityCheckInterval = 2 * time.Second
	}

	fileStabilityCheckCount = 5 // Default value
	if count := os.Getenv("FILE_STABILITY_CHECK_COUNT"); count != "" {
		fmt.Sscanf(count, "%d", &fileStabilityCheckCount)
	}

	retryDelay, err = time.ParseDuration(os.Getenv("RETRY_DELAY"))
	if err != nil {
		retryDelay = 5 * time.Second
	}
}

func watchFiles() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	err = watcher.Add(watchPath)
	if err != nil {
		log.Fatal(err)
	}

	// Check existing files at startup
	files, err := os.ReadDir(watchPath)
	if err != nil {
		log.Fatal(err)
	}
	for _, file := range files {
		if !file.IsDir() {
			fileStabilityConfirmation <- filepath.Join(watchPath, file.Name())
		}
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				fileStabilityConfirmation <- event.Name
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("error:", err)
		}
	}
}

func checkFileStability() {
	for filePath := range fileStabilityConfirmation {
		go func(filePath string) {
			stable := false
			var lastSize int64
			for i := 0; i < fileStabilityCheckCount; i++ {
				fileInfo, err := os.Stat(filePath)
				if err != nil {
					log.Println("error:", err)
					return
				}
				if fileInfo.Size() == lastSize {
					stable = true
					break
				}
				lastSize = fileInfo.Size()
				time.Sleep(fileStabilityCheckInterval)
			}
			if stable {
				readyForUpload <- filePath
			}
		}(filePath)
	}
}

func uploadFiles() {
	for filePath := range readyForUpload {
		go func(filePath string) {
			for {
				err := uploadFile(filePath)
				if err == nil {
					successfulUploads.Inc()
					log.Printf("Successfully uploaded: %s\n", filePath)
					os.Remove(filePath)
					break
				}
				failedUploads.Inc()
				log.Printf("Failed to upload: %s, retrying...\n", filePath)
				time.Sleep(retryDelay)
			}
		}(filePath)
	}
}

func uploadFile(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create the document part
	part, err := writer.CreateFormFile("document", filepath.Base(file.Name()))
	if err != nil {
		return err
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return err
	}

	// Extract the filename and set it as the title
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

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		uploadRetries.Inc()
		return err
	}
	defer resp.Body.Close()

	// Check for HTTP 200 instead of HTTP 201
	if resp.StatusCode != http.StatusOK {
		uploadRetries.Inc()
		responseBody, _ := io.ReadAll(resp.Body)
		log.Printf("Failed to upload document: Status %d, Response: %s", resp.StatusCode, string(responseBody))
		return errors.New("failed to upload document")
	}

	return nil
}
