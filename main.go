package main

import (
	"Go-M3u8-Downloader/database"
	"Go-M3u8-Downloader/downloader"
	"Go-M3u8-Downloader/extractor"
	"Go-M3u8-Downloader/mediaprocess"
	"Go-M3u8-Downloader/telegram"
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/joho/godotenv"
)

type UploadTask struct {
	OutputDir string
	VideoFile string
	ThumbFile string
}

func downloadWorker(id int, jobs <-chan string, uploadJobs chan<- UploadTask, quit <-chan struct{}) {
	for {
		select {
		case raw := <-jobs:
			if raw == "" {
				continue
			}

			url, outputDIR, err := extractor.ExtractURL(raw)
			if err != nil {
				fmt.Println("errorExtracting:", err)
				continue
			}
			if database.IsDownloaded(url) {
				fmt.Printf("[Worker %d] Already downloaded: %s\n", id, url)
				continue
			}

			m3u8URL, thumbFile, err := extractor.GetVideoDetails(url)
			if err != nil {
				fmt.Printf("program cannot find any m3u8 file (exit code %d)", err)
				return
			}

			if m3u8URL == "" {
				fmt.Println("no playlist.m3u8 URL found in output file")
				return
			}

			outVideo := filepath.Join(outputDIR, outputDIR+".mp4")
			if _, err := os.Stat(outVideo); os.IsNotExist(err) {
				fmt.Printf("[Worker %d] Downloading: %s\n", id, raw)
				downloader.StartDownloadTask(m3u8URL, outputDIR, 8)
				fmt.Printf("[Worker %d] Finished: %s\n", id, url)
			}

			fmt.Printf("[MergeWorker %d] Starting to Merge: %s\n", id, outputDIR)
			downloader.MergeFiles(outputDIR, outVideo)
			fmt.Printf("[MergeWorker %d] Merging Finished: %s\n", id, outputDIR)

			// Send upload task to upload worker
			uploadJobs <- UploadTask{
				OutputDir: outputDIR,
				VideoFile: outVideo,
				ThumbFile: thumbFile,
			}

		case <-quit:
			fmt.Printf("[Worker %d] Quitting\n", id)
			return
		}
	}
}

func uploadWorker(uploadJobs <-chan UploadTask, client *gotgproto.Client) {
	for task := range uploadJobs {
		fmt.Printf("[UploadWorker] Started uploading: %s\n", task.OutputDir)

		chatID, err := strconv.Atoi(os.Getenv("TARGET"))
		if err != nil {
			fmt.Println("Error converting TARGET to int:")
			panic(err)
		}

		mediaList, err := mediaprocess.SplitVideoWithFFmpeg(task.VideoFile)
		if err != nil {
			fmt.Println("Error splitting video: ")
			panic(err)
		}

		for _, videoPath := range mediaList {
			config := telegram.Config{
				SessionDB: "video_uploader.db",
				VideoPath: videoPath,
				ChatID:    int64(chatID),
				Caption:   filepath.Base(videoPath),
				Thumbnail: task.ThumbFile, // Optional thumbnail path
			}

			err = telegram.UploadVideo(client, config)
			if err != nil {
				fmt.Println("Error uploading video:")
				panic(err)
			}
		}

		fmt.Printf("[UploadWorker] Finished uploading: %s\n", task.OutputDir)
		mediaprocess.CleanupFiles(mediaList)
		err = os.RemoveAll(filepath.Dir(task.VideoFile))
		if err != nil {
			fmt.Printf("Error removing directory: %v\n", err)
		}
		database.MarkAsDownloaded(task.VideoFile)
		fmt.Printf("[UploadWorker] Marked as downloaded: %s\n", task.VideoFile)
		fmt.Printf("[UploadWorker] Finished processing: %s\n", task.OutputDir)
		fmt.Println("========================================")
	}
}

func parseURLTXT(txtPath string) []string {
	file, err := os.Open(txtPath)
	if err != nil {
		log.Printf("Error opening file %s: ", txtPath)
		panic(err)
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			urls = append(urls, line)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading file %s: ", txtPath)
		panic(err)
	}
	return urls
}

func main() {
	jobs := make(chan string, 2)
	uploadJobs := make(chan UploadTask, 2) // small buffer for uploads
	quit := make(chan struct{})

	err := godotenv.Load(".env")
	if err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	apiID := os.Getenv("API_ID")
	apiHash := os.Getenv("APP_HASH")
	phoneNumber := os.Getenv("PHONE_NUMBER")

	apiIDInt, err := strconv.Atoi(apiID)
	if err != nil {
		log.Fatalln("failed to convert API_ID to int:", err)
	}

	client := telegram.NewTelegramClient(apiIDInt, apiHash, phoneNumber)
	fmt.Printf("client (@%s) has been started...\n", client.Self.Username)

	fmt.Print("Enter number of workers: ")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	workerInput := strings.TrimSpace(scanner.Text())
	workerCount, err := strconv.Atoi(workerInput)
	if err != nil || workerCount <= 0 {
		fmt.Println("Invalid worker count.")
		os.Exit(1)
	}

	// Start download workers
	for i := 1; i <= workerCount; i++ {
		go downloadWorker(i, jobs, uploadJobs, quit)
	}

	// Start single upload worker
	go uploadWorker(uploadJobs, client)

	fmt.Printf("Started %d download workers and 1 upload worker.\n", workerCount)

	if len(os.Args) > 1 {
		txtPath := os.Args[1]
		urls := parseURLTXT(txtPath)
		for _, url := range urls {
			jobs <- url
		}
	} else {
		fmt.Println("🚀 M3U8 Downloader - Enhanced with Upload Queue")
		go func() {
			for {
				downloader.DisplayAllProgress(downloader.RunningTasks)
				time.Sleep(time.Second)
			}
		}()

		for {
			fmt.Print("> ")
			if !scanner.Scan() {
				break
			}
			line := strings.TrimSpace(scanner.Text())

			if strings.EqualFold(line, "exit") {
				close(quit)
				close(jobs)
				close(uploadJobs)
				break
			}

			if line != "" {
				jobs <- line
				fmt.Println("Added to queue:", line)
			}
		}
	}
}
