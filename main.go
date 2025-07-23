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
	version                    = "dev"
	whitelist                  string
	verbose                    bool
)

type FileSystem interface {
	Open(name string) (io.ReadCloser, error)
	ReadDir(dirname string) ([]os.DirEntry, error)
	Stat(name string) (os.FileInfo, error)
	Remove(name string) error
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type RealFileSystem struct{}

func (RealFileSystem) Open(name string) (io.ReadCloser, error) {
	return os.Open(name)
}

func (RealFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	return os.ReadDir(dirname)
}

func (RealFileSystem) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

func (RealFileSystem) Remove(name string) error {
	return os.Remove(name)
}

type RealHTTPClient struct{}

func (RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

func init() {
	prometheus.MustRegister(successfulUploads, failedUploads, uploadRetries)

	os.Setenv("CONSUME_FOLDER", "c:/temp")
	os.Setenv("FILE_CONSUME_WHITELIST", "*.pdf")
	os.Setenv("HTTP_UPLOAD_RETRY_DELAY_SECONDS", "5s")
	os.Setenv("FILE_STABILITY_CHECK_COUNT", "3")
	os.Setenv("FILE_STABILITY_CHECK_INTERVAL_SECONDS", "2s")
	//os.Setenv("PAPERLESS_AUTH_TOKEN", "57d6be2cd6968cf189dafcb989d4610d6274b923")
	//os.Setenv("PAPERLESS_BASE_URL", "http://192.168.2.147:8000")
	//os.Setenv("VERBOSE", "true")
}

func main() {
	log.Println("Starting doc2paperless Version: " + version)

	loadConfig()

	if verbose {
		log.Println("Verbose logging is enabled.")
	}

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/health/liveness", livenessHandler)
	http.HandleFunc("/health/readiness", readinessHandler)

	go func() {
		log.Fatal(http.ListenAndServe(":2112", nil))
	}()

	fs := RealFileSystem{}
	client := RealHTTPClient{}

	go watchFiles(fs)

	go checkFileStability(fs)

	uploadFiles(fs, client)

	select {} // Block forever
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
		log.Fatal("Environment Variable PAPERLESS_AUTH_TOKEN not set. Note: Currently only Auth token are supported, not Base64(user:pass)")
	}

	fileStabilityCheckInterval, err = time.ParseDuration(os.Getenv("FILE_STABILITY_CHECK_INTERVAL_SECONDS"))
	if err != nil {
		fileStabilityCheckInterval = 2 * time.Second
	}

	fileStabilityCheckCount = 5
	if count := os.Getenv("FILE_STABILITY_CHECK_COUNT"); count != "" {
		fmt.Sscanf(count, "%d", &fileStabilityCheckCount)
	}

	retryDelay, err = time.ParseDuration(os.Getenv("HTTP_UPLOAD_RETRY_DELAY_SECONDS"))
	if err != nil {
		retryDelay = 5 * time.Second
	}

	verboseStr := os.Getenv("VERBOSE")
	verbose = false

	if verboseStr != "" {
		parsedVerbose, err := strconv.ParseBool(verboseStr)
		if err == nil {
			verbose = parsedVerbose
		}
	}
}

func livenessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	// Implement a real readiness check if needed
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func watchFiles(fs FileSystem) {
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
		if !file.IsDir() && isWhitelisted(file.Name()) {
			fileStabilityConfirmation <- filepath.Join(watchPath, file.Name())
		}
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create && isWhitelisted(event.Name) {
				log.Println("Detected new file. Starting stability check for: " + event.Name)
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

func checkFileStability(fs FileSystem) {
	for filePath := range fileStabilityConfirmation {
		go func(filePath string) {
			var lastSize int64
			consecutiveStableCount := 0

			for {
				if verbose {
					log.Println("Checking stability for " + filePath + " Consecutive readings with same size: " + strconv.Itoa(consecutiveStableCount) + "/" + strconv.Itoa(fileStabilityCheckCount))
				}

				fileInfo, err := fs.Stat(filePath)
				if err != nil {
					log.Println("error:", err)
					return
				}

				currentSize := fileInfo.Size()
				if currentSize == lastSize {
					consecutiveStableCount++
					if consecutiveStableCount >= fileStabilityCheckCount {
						if verbose {
							log.Println(fmt.Sprintf("Checking stability for %s: Consecutive readings with same size: %d/%d -> OK, ready for Upload.", filePath, consecutiveStableCount, fileStabilityCheckCount))
						}
						readyForUpload <- filePath
						return
					}
				} else {
					consecutiveStableCount = 0
				}

				lastSize = currentSize
				time.Sleep(fileStabilityCheckInterval)
			}
		}(filePath)
	}
}

func uploadFiles(fs FileSystem, client HTTPClient) {
	for filePath := range readyForUpload {
		go func(filePath string) {
			for {
				err := uploadFile(fs, client, filePath)
				if err == nil {
					successfulUploads.Inc()
					log.Printf("Successfully uploaded: %s\n", filePath)
					fs.Remove(filePath)
					break
				}
				failedUploads.Inc()
				log.Printf("Failed to upload: %s, retrying...\n", filePath)
				time.Sleep(retryDelay)
			}
		}(filePath)
	}
}

func uploadFile(fs FileSystem, client HTTPClient, filePath string) error {
	fileReader, err := fs.Open(filePath)
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
		uploadRetries.Inc()
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		uploadRetries.Inc()
		responseBody, _ := io.ReadAll(resp.Body)
		log.Printf("Failed to upload document: Status %d, Response: %s", resp.StatusCode, string(responseBody))
		return errors.New("failed to upload document")
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
