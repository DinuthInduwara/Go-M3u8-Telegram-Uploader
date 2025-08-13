package main

import (
	"Go-M3u8-Downloader/downloader"
	"Go-M3u8-Downloader/extractor"
	"Go-M3u8-Downloader/mediaprocess"
	"Go-M3u8-Downloader/telegram"
	"bufio"
	"fmt"
	"github.com/celestix/gotgproto"
	"github.com/joho/godotenv"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func downloadWorker(id int, jobs <-chan string, quit <-chan struct{}, client *gotgproto.Client) {
	for {
		select {
		case raw := <-jobs:
			if raw == "" {
				continue
			}
			fmt.Printf("[Worker %d] Downloading: %s\n", id, raw)

			url, outputDIR, err := extractor.ExtractURL(raw)
			if err != nil {
				fmt.Println("errorExtracting:", err)
				continue
			}
			m3u8URL, err := extractor.GrabM3u8URL(url)
			if err != nil {
				fmt.Println("errorGrabbing:", err)
				continue
			}

			downloader.StartDownloadTask(m3u8URL, outputDIR, 8)
			outVIdeo := filepath.Join(outputDIR, outputDIR+".mp4")
			fmt.Printf("[Worker %d] Finished: %s\n", id, url)
			fmt.Printf("[MergWorker %d] Starting to Merging Video: %s\n", id, outputDIR)
			downloader.MergeFiles(outputDIR, outVIdeo)
			fmt.Printf("[MergWorker %d] Merging Finished: %s\n", id, outputDIR)
			fmt.Printf("[UploadWorker %d] Started To Upload: %s\n", id, outputDIR)
			chatID, err := strconv.Atoi(os.Getenv("TARGET"))
			if err != nil {
				panic(err)
			}
			mediaList, err := mediaprocess.SplitVideoWithFFmpeg(outVIdeo)
			if err != nil {
				panic(err)
			}

			for _, videoPath := range mediaList {
				config := telegram.Config{
					SessionDB: "video_uploader.db", // SQLite session database
					VideoPath: videoPath,           // Path to your video file
					ChatID:    int64(chatID),       // Target chat ID (use @username for channels/groups)
					Caption:   "OCDS:" + videoPath, // Video caption
					Thumbnail: "",                  // Optional: path to thumbnail image
				}

				err = telegram.UploadVideo(client, config)
			}
			fmt.Printf("[UploadWorker %d] Finish Uploading: %s\n", id, outputDIR)
			mediaprocess.CleanupFiles(mediaList)
		case <-quit:
			fmt.Printf("[Worker %d] Quitting\n", id)
			return
		}
	}
}

func main() {

	jobs := make(chan string, 20)
	quit := make(chan struct{})

	err := godotenv.Load(".env")
	if err != nil {
		log.Printf("Error loading .env file: %v. Proceeding without .env variables.", err)
	}
	apiID := os.Getenv("API_ID")
	apiHash := os.Getenv("APP_HASH")
	phoneNumber := os.Getenv("PHONE_NUMBER")

	fmt.Println("Video uploaded successfully!")

	apiIDInt, err := strconv.Atoi(apiID)
	if err != nil {
		log.Fatalln("failed to convert API_ID to int:", err)
	}
	client := telegram.NewTelegramClient(apiIDInt, apiHash, phoneNumber)
	fmt.Printf("client (@%s) has been started...\n", client.Self.Username)

	if len(os.Args) < 2 {
		fmt.Println("🚀 M3U8 Downloader - Enhanced with Progress")
		fmt.Println("Usage:  <m3u8_url> ")
		fmt.Println("Example: https://missav.ws/en/piyo-186")
		fmt.Println("\nFeatures:")
		fmt.Println("  ✅ Concurrent downloads")
		fmt.Println("  ✅ Progress tracking")
		fmt.Println("  ✅ Automatic retry on failures")
		fmt.Println("  ✅ Resume partial downloads")
	}

	fmt.Print("Enter number of workers: ")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	workerInput := strings.TrimSpace(scanner.Text())
	workerCount, err := strconv.Atoi(workerInput)
	if err != nil || workerCount <= 0 {
		fmt.Println("Invalid worker count.")
		os.Exit(1)
	}

	for i := 1; i <= workerCount; i++ {
		go downloadWorker(i, jobs, quit, client)
	}
	fmt.Printf("Started %d workers.\n", workerCount)

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
			break
		}

		if line != "" {
			jobs <- line
			fmt.Println("Added to queue:", line)
		}

	}

}
