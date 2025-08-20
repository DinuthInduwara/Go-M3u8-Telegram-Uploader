package main

import (
	"Go-M3u8-Downloader/database"
	"Go-M3u8-Downloader/downloader"
	"Go-M3u8-Downloader/extractor"
	"Go-M3u8-Downloader/mediaprocess"
	"Go-M3u8-Downloader/telegram"
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/joho/godotenv"
)

// Job represents a download job with metadata
type Job struct {
	ID       int
	URL      string
	Status   string
	Created  time.Time
	Started  *time.Time
	Finished *time.Time
}

// DownloadResult represents the result of a download operation
type DownloadResult struct {
	Job       Job
	OutputDir string
	VideoFile string
	ThumbFile string
	Error     error
}

// UploadTask represents an upload task
type UploadTask struct {
	Job       Job
	OutputDir string
	VideoFile string
	ThumbFile string
}

// UploadResult represents the result of an upload operation
type UploadResult struct {
	Task  UploadTask
	Error error
}

// Pipeline manages the entire download-upload pipeline
type Pipeline struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *gotgproto.Client
	config Config

	// Channels for pipeline stages
	jobQueue      chan Job
	downloadQueue chan Job
	uploadQueue   chan DownloadResult
	resultQueue   chan UploadResult

	// Metrics and monitoring
	stats  *PipelineStats
	logger *Logger

	wg sync.WaitGroup
}

// Config holds pipeline configuration
type Config struct {
	WorkerCount    int
	QueueSize      int
	TargetChatID   int64
	SessionDB      string
	OutputDir      string  // Added for custom output directory
	UploadToTG     bool    // Added for telegram upload control
}

// PipelineStats tracks pipeline metrics
type PipelineStats struct {
	mu              sync.RWMutex
	TotalJobs       int
	CompletedJobs   int
	FailedJobs      int
	ActiveDownloads int
	ActiveUploads   int
	StartTime       time.Time
}

// Logger provides structured logging
type Logger struct {
	*log.Logger
}

func NewLogger() *Logger {
	return &Logger{
		Logger: log.New(os.Stdout, "", log.LstdFlags),
	}
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.Printf("[INFO] "+format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.Printf("[ERROR] "+format, args...)
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.Printf("[DEBUG] "+format, args...)
}

func (l *Logger) JobStatus(job Job, status string, args ...interface{}) {
	prefix := fmt.Sprintf("[JOB-%d] %s: ", job.ID, status)
	l.Printf(prefix+fmt.Sprintf("%v", args...), args[1:]...)
}

// NewPipeline creates a new pipeline instance
func NewPipeline(client *gotgproto.Client, config Config) *Pipeline {
	ctx, cancel := context.WithCancel(context.Background())

	return &Pipeline{
		ctx:           ctx,
		cancel:        cancel,
		client:        client,
		config:        config,
		jobQueue:      make(chan Job, config.QueueSize),
		downloadQueue: make(chan Job, config.QueueSize),
		uploadQueue:   make(chan DownloadResult, config.QueueSize),
		resultQueue:   make(chan UploadResult, config.QueueSize),
		stats: &PipelineStats{
			StartTime: time.Now(),
		},
		logger: NewLogger(),
	}
}

// Start initializes and starts the pipeline
func (p *Pipeline) Start() error {
	p.logger.Info("Starting M3U8 Download Pipeline")
	p.logger.Info("Configuration: Workers=%d, QueueSize=%d, OutputDir=%s, UploadToTG=%t", 
		p.config.WorkerCount, p.config.QueueSize, p.config.OutputDir, p.config.UploadToTG)

	// Start pipeline stages
	p.startJobDispatcher()
	p.startDownloadWorkers()
	
	// Only start upload worker if telegram upload is enabled
	if p.config.UploadToTG {
		p.startUploadWorker()
	} else {
		// If not uploading to telegram, directly process download results
		p.startDownloadOnlyProcessor()
	}
	
	p.startResultProcessor()
	p.startStatsMonitor()

	p.logger.Info("Pipeline started successfully")
	return nil
}

// Stop gracefully shuts down the pipeline
func (p *Pipeline) Stop() {
	p.logger.Info("Shutting down pipeline...")
	p.cancel()

	// Close channels in reverse order
	close(p.jobQueue)
	p.wg.Wait()

	p.logger.Info("Pipeline shutdown complete")
	p.printFinalStats()
}

// SubmitJob adds a new job to the pipeline
func (p *Pipeline) SubmitJob(url string) {
	p.stats.mu.Lock()
	p.stats.TotalJobs++
	jobID := p.stats.TotalJobs
	p.stats.mu.Unlock()

	job := Job{
		ID:      jobID,
		URL:     url,
		Status:  "queued",
		Created: time.Now(),
	}

	select {
	case p.jobQueue <- job:
		p.logger.JobStatus(job, "QUEUED", "URL: %s", url)
	case <-p.ctx.Done():
		p.logger.Error("Failed to queue job - pipeline shutting down")
	}
}

// startJobDispatcher manages job distribution
func (p *Pipeline) startJobDispatcher() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(p.downloadQueue)

		p.logger.Info("Job dispatcher started")

		for {
			select {
			case job, ok := <-p.jobQueue:
				if !ok {
					p.logger.Info("Job queue closed, stopping dispatcher")
					return
				}

				// Extract URL and check custom output directory
				url, outputDir, err := extractor.ExtractURL(job.URL)
				if err != nil {
					p.logger.JobStatus(job, "FAILED", "URL extraction failed: %v", err)
					continue
				}

				// Use custom output directory if specified
				if p.config.OutputDir != "./" {
					// Create custom output directory structure
					customOutputDir := filepath.Join(p.config.OutputDir, outputDir)
					outputDir = customOutputDir
				}

				if database.IsDownloaded(outputDir) {
					p.logger.JobStatus(job, "SKIPPED", "Already downloaded: %s", url)
					p.incrementCompleted()
					continue
				}

				job.Status = "dispatched"
				select {
				case p.downloadQueue <- job:
					p.logger.JobStatus(job, "DISPATCHED", "Sent to download queue")
				case <-p.ctx.Done():
					return
				}

			case <-p.ctx.Done():
				p.logger.Info("Job dispatcher stopping")
				return
			}
		}
	}()
}

// startDownloadWorkers starts multiple download workers
func (p *Pipeline) startDownloadWorkers() {
	for i := 1; i <= p.config.WorkerCount; i++ {
		p.wg.Add(1)
		go p.downloadWorker(i)
	}
}

// downloadWorker processes download jobs
func (p *Pipeline) downloadWorker(workerID int) {
	defer p.wg.Done()

	p.logger.Info("Download worker %d started", workerID)

	for {
		select {
		case job, ok := <-p.downloadQueue:
			if !ok {
				p.logger.Info("Download worker %d: queue closed", workerID)
				return
			}

			p.processDownloadJob(workerID, job)

		case <-p.ctx.Done():
			p.logger.Info("Download worker %d stopping", workerID)
			return
		}
	}
}

// processDownloadJob handles individual download job processing
func (p *Pipeline) processDownloadJob(workerID int, job Job) {
	p.incrementActiveDownloads(1)
	defer p.incrementActiveDownloads(-1)

	now := time.Now()
	job.Started = &now
	job.Status = "downloading"

	p.logger.JobStatus(job, "STARTED", "Worker %d processing", workerID)

	result := DownloadResult{Job: job}

	// Extract URL and output directory
	url, outputDir, err := extractor.ExtractURL(job.URL)
	if err != nil {
		result.Error = fmt.Errorf("URL extraction failed: %w", err)
		p.sendDownloadResult(result)
		return
	}

	// Use custom output directory if specified
	if p.config.OutputDir != "./" {
		// Create custom output directory structure
		customOutputDir := filepath.Join(p.config.OutputDir, outputDir)
		outputDir = customOutputDir
		
		// Ensure the custom directory exists
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			result.Error = fmt.Errorf("failed to create output directory: %w", err)
			p.sendDownloadResult(result)
			return
		}
	}

	result.OutputDir = outputDir

	// Get video details
	p.logger.JobStatus(job, "EXTRACTING", "Getting video details")
	m3u8URL, thumbFile, err := extractor.GetVideoDetails(url)
	if err != nil {
		result.Error = fmt.Errorf("failed to get video details: %w", err)
		p.sendDownloadResult(result)
		return
	}

	if m3u8URL == "" {
		result.Error = fmt.Errorf("no playlist.m3u8 URL found")
		p.sendDownloadResult(result)
		return
	}

	result.ThumbFile = thumbFile
	result.VideoFile = filepath.Join(outputDir, filepath.Base(outputDir)+".mp4")

	// Check if video already exists
	if _, err := os.Stat(result.VideoFile); !os.IsNotExist(err) {
		p.logger.JobStatus(job, "EXISTS", "Video file already exists: %s", result.VideoFile)
	} else {
		// Download video segments
		p.logger.JobStatus(job, "DOWNLOADING", "Starting segment download from: %s", m3u8URL)
		downloader.StartDownloadTask(m3u8URL, outputDir, 8)
		p.logger.JobStatus(job, "DOWNLOAD_COMPLETE", "Segment download finished")
	}

	// Merge video files
	p.logger.JobStatus(job, "MERGING", "Starting file merge")
	downloader.MergeFiles(outputDir, result.VideoFile)
	p.logger.JobStatus(job, "MERGE_COMPLETE", "File merge finished: %s", result.VideoFile)

	// Mark job as completed
	finished := time.Now()
	result.Job.Finished = &finished
	result.Job.Status = "downloaded"

	duration := finished.Sub(*job.Started)
	p.logger.JobStatus(job, "COMPLETED", "Download finished in %v", duration)

	p.sendDownloadResult(result)
}

// sendDownloadResult sends result to upload queue or download-only processor
func (p *Pipeline) sendDownloadResult(result DownloadResult) {
	select {
	case p.uploadQueue <- result:
		if result.Error != nil {
			p.logger.JobStatus(result.Job, "ERROR", "Download failed: %v", result.Error)
			p.incrementFailed()
		} else {
			if p.config.UploadToTG {
				p.logger.JobStatus(result.Job, "QUEUED_UPLOAD", "Sent to upload queue")
			} else {
				p.logger.JobStatus(result.Job, "DOWNLOAD_ONLY", "Download completed, skipping upload")
			}
		}
	case <-p.ctx.Done():
		p.logger.Error("Failed to send download result - pipeline shutting down")
	}
}

// startDownloadOnlyProcessor handles downloads when upload is disabled
func (p *Pipeline) startDownloadOnlyProcessor() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(p.resultQueue)

		p.logger.Info("Download-only processor started (Telegram upload disabled)")

		for {
			select {
			case result, ok := <-p.uploadQueue:
				if !ok {
					p.logger.Info("Upload queue closed, stopping download-only processor")
					return
				}

				// Create result for download-only mode
				uploadResult := UploadResult{
					Task: UploadTask{
						Job:       result.Job,
						OutputDir: result.OutputDir,
						VideoFile: result.VideoFile,
						ThumbFile: result.ThumbFile,
					},
					Error: result.Error,
				}

				if result.Error == nil {
					uploadResult.Task.Job.Status = "completed"
					finished := time.Now()
					uploadResult.Task.Job.Finished = &finished

					totalDuration := finished.Sub(uploadResult.Task.Job.Created)
					p.logger.JobStatus(uploadResult.Task.Job, "DOWNLOAD_COMPLETE", "Download completed in %v (kept locally)", totalDuration)

					database.MarkAsDownloaded(filepath.Dir(uploadResult.Task.VideoFile))
					p.logger.JobStatus(uploadResult.Task.Job, "MARKED_COMPLETE", "Marked as downloaded in database")
				}

				p.sendUploadResult(uploadResult)

			case <-p.ctx.Done():
				p.logger.Info("Download-only processor stopping")
				return
			}
		}
	}()
}

// startUploadWorker starts the upload worker
func (p *Pipeline) startUploadWorker() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(p.resultQueue)

		p.logger.Info("Upload worker started")

		for {
			select {
			case result, ok := <-p.uploadQueue:
				if !ok {
					p.logger.Info("Upload queue closed, stopping upload worker")
					return
				}

				if result.Error != nil {
					// Skip upload for failed downloads
					uploadResult := UploadResult{
						Task:  UploadTask{Job: result.Job},
						Error: result.Error,
					}
					p.sendUploadResult(uploadResult)
					continue
				}

				p.processUploadTask(result)

			case <-p.ctx.Done():
				p.logger.Info("Upload worker stopping")
				return
			}
		}
	}()
}

// processUploadTask handles individual upload task processing
func (p *Pipeline) processUploadTask(downloadResult DownloadResult) {
	p.incrementActiveUploads(1)
	defer p.incrementActiveUploads(-1)

	task := UploadTask{
		Job:       downloadResult.Job,
		OutputDir: downloadResult.OutputDir,
		VideoFile: downloadResult.VideoFile,
		ThumbFile: downloadResult.ThumbFile,
	}

	task.Job.Status = "uploading"
	p.logger.JobStatus(task.Job, "UPLOAD_STARTED", "Processing: %s", task.VideoFile)

	uploadResult := UploadResult{Task: task}

	// Split video if needed
	p.logger.JobStatus(task.Job, "SPLITTING", "Splitting video with FFmpeg")
	mediaList, err := mediaprocess.SplitVideoWithFFmpeg(task.VideoFile)
	if err != nil {
		uploadResult.Error = fmt.Errorf("video splitting failed: %w", err)
		p.sendUploadResult(uploadResult)
		return
	}

	p.logger.JobStatus(task.Job, "SPLIT_COMPLETE", "Video split into %d parts", len(mediaList))

	// Upload each part
	for i, videoPath := range mediaList {
		p.logger.JobStatus(task.Job, "UPLOADING_PART", "Part %d/%d: %s", i+1, len(mediaList), filepath.Base(videoPath))

		config := telegram.Config{
			SessionDB: p.config.SessionDB,
			VideoPath: videoPath,
			ChatID:    p.config.TargetChatID,
			Caption:   filepath.Base(videoPath),
			Thumbnail: task.ThumbFile,
		}

		err = telegram.UploadVideo(p.client, config)
		if err != nil {
			uploadResult.Error = fmt.Errorf("upload failed for part %d: %w", i+1, err)
			break
		}

		p.logger.JobStatus(task.Job, "PART_UPLOADED", "Part %d/%d uploaded successfully", i+1, len(mediaList))
	}

	if uploadResult.Error == nil {
		task.Job.Status = "completed"
		finished := time.Now()
		task.Job.Finished = &finished

		totalDuration := finished.Sub(task.Job.Created)
		p.logger.JobStatus(task.Job, "UPLOAD_COMPLETE", "All parts uploaded successfully (total time: %v)", totalDuration)

		// Cleanup only if uploading to Telegram
		if p.config.UploadToTG {
			p.logger.JobStatus(task.Job, "CLEANING", "Cleaning up temporary files")
			mediaprocess.CleanupFiles(mediaList)

			if err := os.RemoveAll(filepath.Dir(task.VideoFile)); err != nil {
				p.logger.JobStatus(task.Job, "CLEANUP_WARNING", "Failed to remove directory: %v", err)
			}
		}

		database.MarkAsDownloaded(filepath.Dir(task.VideoFile))
		p.logger.JobStatus(task.Job, "MARKED_COMPLETE", "Marked as downloaded in database")
	}

	p.sendUploadResult(uploadResult)
}

// sendUploadResult sends result to result processor
func (p *Pipeline) sendUploadResult(result UploadResult) {
	select {
	case p.resultQueue <- result:
	case <-p.ctx.Done():
	}
}

// startResultProcessor processes final results
func (p *Pipeline) startResultProcessor() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		p.logger.Info("Result processor started")

		for {
			select {
			case result, ok := <-p.resultQueue:
				if !ok {
					p.logger.Info("Result queue closed, stopping result processor")
					return
				}

				if result.Error != nil {
					p.logger.JobStatus(result.Task.Job, "FAILED", "Final error: %v", result.Error)
					p.incrementFailed()
				} else {
					p.logger.JobStatus(result.Task.Job, "SUCCESS", "Job completed successfully")
					p.incrementCompleted()
				}

				p.logger.Info("========================================")

			case <-p.ctx.Done():
				p.logger.Info("Result processor stopping")
				return
			}
		}
	}()
}

// startStatsMonitor displays periodic statistics
func (p *Pipeline) startStatsMonitor() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				p.printStats()
			case <-p.ctx.Done():
				return
			}
		}
	}()
}

// Helper methods for stats
func (p *Pipeline) incrementActiveDownloads(delta int) {
	p.stats.mu.Lock()
	p.stats.ActiveDownloads += delta
	p.stats.mu.Unlock()
}

func (p *Pipeline) incrementActiveUploads(delta int) {
	p.stats.mu.Lock()
	p.stats.ActiveUploads += delta
	p.stats.mu.Unlock()
}

func (p *Pipeline) incrementCompleted() {
	p.stats.mu.Lock()
	p.stats.CompletedJobs++
	p.stats.mu.Unlock()
}

func (p *Pipeline) incrementFailed() {
	p.stats.mu.Lock()
	p.stats.FailedJobs++
	p.stats.mu.Unlock()
}

// printStats displays current pipeline statistics
func (p *Pipeline) printStats() {
	p.stats.mu.RLock()
	defer p.stats.mu.RUnlock()

	uptime := time.Since(p.stats.StartTime)

	fmt.Printf("\n📊 Pipeline Statistics (Uptime: %v)\n", uptime.Truncate(time.Second))
	fmt.Printf("├── Total Jobs: %d\n", p.stats.TotalJobs)
	fmt.Printf("├── Completed: %d\n", p.stats.CompletedJobs)
	fmt.Printf("├── Failed: %d\n", p.stats.FailedJobs)
	fmt.Printf("├── Active Downloads: %d\n", p.stats.ActiveDownloads)
	
	if p.config.UploadToTG {
		fmt.Printf("├── Active Uploads: %d\n", p.stats.ActiveUploads)
	}

	pending := p.stats.TotalJobs - p.stats.CompletedJobs - p.stats.FailedJobs
	fmt.Printf("└── Pending: %d\n", pending)
	fmt.Println()
}

// printFinalStats displays final statistics when shutting down
func (p *Pipeline) printFinalStats() {
	p.stats.mu.RLock()
	defer p.stats.mu.RUnlock()

	uptime := time.Since(p.stats.StartTime)

	fmt.Println("\n🏁 Final Pipeline Statistics")
	fmt.Printf("├── Total Runtime: %v\n", uptime.Truncate(time.Second))
	fmt.Printf("├── Total Jobs: %d\n", p.stats.TotalJobs)
	fmt.Printf("├── Completed: %d\n", p.stats.CompletedJobs)
	fmt.Printf("├── Failed: %d\n", p.stats.FailedJobs)

	if p.stats.TotalJobs > 0 {
		successRate := float64(p.stats.CompletedJobs) / float64(p.stats.TotalJobs) * 100
		fmt.Printf("└── Success Rate: %.1f%%\n", successRate)
	}
	fmt.Println()
}

// parseURLTXT reads URLs from a text file
func parseURLTXT(txtPath string) ([]string, error) {
	file, err := os.Open(txtPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", txtPath, err)
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			urls = append(urls, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", txtPath, err)
	}

	return urls, nil
}

// loadConfig loads configuration from environment variables and command-line flags
func loadConfig(outputDir string, uploadToTG bool) (Config, error) {
	err := godotenv.Load(".env")
	if err != nil {
		log.Printf("Warning: Error loading .env file: %v", err)
	}

	config := Config{
		WorkerCount:  3, // Default value, will be overridden by user input
		QueueSize:    3,
		SessionDB:    "video_uploader.db",
		OutputDir:    outputDir,
		UploadToTG:   uploadToTG,
	}

	// Only require TARGET if uploading to Telegram
	if uploadToTG {
		targetStr := os.Getenv("TARGET")
		if targetStr == "" {
			return Config{}, fmt.Errorf("TARGET environment variable is required when using -tg flag")
		}

		targetChatID, err := strconv.ParseInt(targetStr, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("invalid TARGET value: %w", err)
		}
		config.TargetChatID = targetChatID
	}

	return config, nil
}

// setupTelegramClient initializes the Telegram client (only when needed)
func setupTelegramClient() (*gotgproto.Client, error) {
	apiID := os.Getenv("API_ID")
	apiHash := os.Getenv("APP_HASH")
	phoneNumber := os.Getenv("PHONE_NUMBER")

	if apiID == "" || apiHash == "" || phoneNumber == "" {
		return nil, fmt.Errorf("API_ID, APP_HASH, and PHONE_NUMBER environment variables are required")
	}

	apiIDInt, err := strconv.Atoi(apiID)
	if err != nil {
		return nil, fmt.Errorf("invalid API_ID: %w", err)
	}

	client := telegram.NewTelegramClient(apiIDInt, apiHash, phoneNumber)
	fmt.Printf("✅ Telegram client (@%s) initialized successfully\n", client.Self.Username)

	return client, nil
}

func main() {
	// Define command-line flags
	output := flag.String("output", "./", "Output Directory")
	uploadToTG := flag.Bool("tg", false, "Upload To Telegram And Delete Local Files")
	
	// Parse command-line flags
	flag.Parse()

	fmt.Println("🚀 M3U8 Downloader - Enhanced Pipeline Architecture")
	fmt.Println("==================================================")
	
	// Display configuration
	fmt.Printf("📁 Output Directory: %s\n", *output)
	if *uploadToTG {
		fmt.Println("📤 Telegram Upload: Enabled (files will be deleted after upload)")
	} else {
		fmt.Println("💾 Local Mode: Files will be kept locally")
	}
	fmt.Println()

	// Load configuration with flag values
	config, err := loadConfig(*output, *uploadToTG)
	if err != nil {
		log.Fatal("Configuration error:", err)
	}

	// Setup Telegram client only if needed
	var client *gotgproto.Client
	if *uploadToTG {
		client, err = setupTelegramClient()
		if err != nil {
			log.Fatal("Telegram client error:", err)
		}
	} else {
		fmt.Println("ℹ️  Telegram client not initialized (local mode)")
	}

	// Get worker count from user
	fmt.Print("Enter number of download workers (default: 3): ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		workerInput := strings.TrimSpace(scanner.Text())
		if workerInput != "" {
			if workerCount, err := strconv.Atoi(workerInput); err == nil && workerCount > 0 {
				config.WorkerCount = workerCount
			} else {
				fmt.Println("Invalid worker count, using default: 3")
				config.WorkerCount = 3
			}
		}
	}

	// Create and start pipeline
	pipeline := NewPipeline(client, config)
	if err := pipeline.Start(); err != nil {
		log.Fatal("Failed to start pipeline:", err)
	}

	// Handle shutdown gracefully
	defer pipeline.Stop()

	// Process input - handle remaining args after flags
	args := flag.Args()
	if len(args) > 0 {
		// Batch mode - process file
		txtPath := args[0]
		urls, err := parseURLTXT(txtPath)
		if err != nil {
			log.Fatal("Failed to parse URL file:", err)
		}

		fmt.Printf("📄 Loaded %d URLs from file: %s\n", len(urls), txtPath)
		for _, url := range urls {
			pipeline.SubmitJob(url)
		}

		// Wait for all jobs to complete
		for {
			time.Sleep(time.Second)
			pipeline.stats.mu.RLock()
			pending := pipeline.stats.TotalJobs - pipeline.stats.CompletedJobs - pipeline.stats.FailedJobs
			pipeline.stats.mu.RUnlock()

			if pending == 0 {
				break
			}
		}
	} else {
		// Interactive mode
		fmt.Println("💬 Interactive mode - Enter URLs or 'exit' to quit")
		fmt.Println("Commands:")
		fmt.Println("  - Enter a URL to download")
		fmt.Println("  - Type 'stats' to show current statistics")
		fmt.Println("  - Type 'exit' to quit")
		fmt.Println()

		for {
			fmt.Print("> ")
			if !scanner.Scan() {
				break
			}

			line := strings.TrimSpace(scanner.Text())

			switch strings.ToLower(line) {
			case "exit", "quit":
				fmt.Println("👋 Shutting down...")
				return
			case "stats":
				pipeline.printStats()
			case "":
				continue
			default:
				pipeline.SubmitJob(line)
			}
		}
	}
}