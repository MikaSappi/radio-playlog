package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var homeDir string

func init() {
	var err error
	homeDir, err = homedir.Dir()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("homeDir is: %s", homeDir)
}

func main() {
	dataDir := filepath.Join(homeDir, "Radio-10", "opt", "logger", "data")
	archiveDir := filepath.Join(homeDir, "Radio-10", "opt", "logger", "archives")

	log.Printf("dataDir will be: %s", dataDir)
	log.Printf("archiveDir will be: %s", archiveDir)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		log.Fatal(err)
	}

	go archiveWatcher(dataDir, archiveDir)

	// Start HTTP server
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleHTTPLog(w, r, dataDir)
	})
	go func() {
		log.Printf("HTTP server listening on port 9201")
		if err := http.ListenAndServe(":9201", nil); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Start TCP server
	listener, err := net.Listen("tcp", ":9200")
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("TCP server listening on port 9200")

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		go handleConnection(conn, dataDir)
	}
}

func handleHTTPLog(w http.ResponseWriter, r *http.Request, dataDir string) {
	log.Printf("HTTP request received: method=%s path=%s remote=%s", r.Method, r.URL.Path, r.RemoteAddr)

	if r.Method != http.MethodPost {
		log.Printf("Rejected non-POST method: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Failed to read body: %v", err)
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	message := string(body)
	log.Printf("Received message (length=%d): %s", len(message), message)

	if message == "" {
		log.Printf("Rejected empty message")
		http.Error(w, "Empty message", http.StatusBadRequest)
		return
	}

	if err := writeLog(message, dataDir); err != nil {
		log.Printf("HTTP log error: %v", err)
		http.Error(w, "Failed to write log", http.StatusInternalServerError)
		return
	}

	log.Printf("Successfully logged HTTP message")
	w.WriteHeader(http.StatusOK)
}

func handleConnection(conn net.Conn, dataDir string) {
	defer conn.Close()

	log.Printf("TCP connection from: %s", conn.RemoteAddr())

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	scanner := bufio.NewScanner(conn)

	if scanner.Scan() {
		line := scanner.Text()
		log.Printf("TCP received (length=%d): %s", len(line), line)
		if line != "" {
			if err := writeLog(line, dataDir); err != nil {
				log.Printf("TCP log error: %v", err)
			} else {
				log.Printf("Successfully logged TCP message")
			}
		}
	}
}

func writeLog(message string, dataDir string) error {
	timestamp := time.Now().Format(time.RFC3339)
	filename := filepath.Join(dataDir, time.Now().Format("2006-01.csv"))

	// Parse JSON input
	var data map[string]string
	if err := json.Unmarshal([]byte(message), &data); err != nil {
		return fmt.Errorf("failed to parse JSON: %v", err)
	}

	title := data["title"]
	artist := data["artist"]

	// Check if file exists to determine if we need header
	fileExists := true
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		fileExists = false
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write header if new file
	if !fileExists {
		if _, err := f.WriteString("timestamp,title,artist\n"); err != nil {
			return err
		}
	}

	// Escape CSV fields (wrap in quotes if contains comma, quote, or newline)
	title = escapeCSV(title)
	artist = escapeCSV(artist)

	logEntry := fmt.Sprintf("%s,%s,%s\n", timestamp, title, artist)
	_, err = f.WriteString(logEntry)
	return err
}

func escapeCSV(field string) string {
	if strings.ContainsAny(field, ",\"\n") {
		field = strings.ReplaceAll(field, "\"", "\"\"")
		return fmt.Sprintf("\"%s\"", field)
	}
	return field
}

func archiveWatcher(dataDir, archiveDir string) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	lastMonth := time.Now().Format("2006-01")

	for range ticker.C {
		currentMonth := time.Now().Format("2006-01")

		if currentMonth != lastMonth {
			archiveMonth(lastMonth, dataDir, archiveDir)
			lastMonth = currentMonth
		}
	}
}

func archiveMonth(month string, dataDir, archiveDir string) {
	filename := filepath.Join(dataDir, month+".csv")

	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return
	}

	zipPath := filepath.Join(archiveDir, month+".zip")

	zipFile, err := os.Create(zipPath)
	if err != nil {
		log.Printf("Failed to create zip: %v", err)
		return
	}
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	file, err := os.Open(filename)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
		return
	}
	defer file.Close()

	writer, err := archive.Create(filepath.Base(filename))
	if err != nil {
		log.Printf("Failed to create zip entry: %v", err)
		return
	}

	if _, err := io.Copy(writer, file); err != nil {
		log.Printf("Failed to write to zip: %v", err)
		return
	}

	archive.Close()
	zipFile.Close()
	file.Close()

	if err := os.Remove(filename); err != nil {
		log.Printf("Failed to remove original file: %v", err)
	}

	log.Printf("Archived %s to %s", filename, zipPath)
}
