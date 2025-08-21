package downloader

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var RunningTasks []*OverallProgress

// ensureDir creates a directory if it doesn't exist
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	return nil
}

// downloadFile downloads a single file using Go's HTTP client
func downloadFile(url string, filepath string) error {
	fmt.Printf("📥 Downloading: %s\n", url)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s (code: %d)", resp.Status, resp.StatusCode)
	}

	outFile, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filepath, err)
	}
	defer outFile.Close()

	bytesWritten, err := io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %w", filepath, err)
	}
	fmt.Printf("✅ Downloaded %d bytes to: %s\n", bytesWritten, filepath)
	return nil
}

// downloadSegment downloads a single segment without individual progress tracking
func downloadSegment(url string, filepath string, segmentIndex int, progressTracker *OverallProgress) error {
	// Check if file already exists and has content to avoid overwriting
	if info, err := os.Stat(filepath); err == nil && info.Size() > 0 {
		progressTracker.UpdateSegment(segmentIndex, true, info.Size())
		return nil
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download segment %04d from %s: %w", segmentIndex, url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status for segment %04d: %s (code: %d)", segmentIndex, resp.Status, resp.StatusCode)
	}

	// Create a temporary file first to avoid partial writes
	tempFilepath := filepath + ".tmp"
	outFile, err := os.Create(tempFilepath)
	if err != nil {
		return fmt.Errorf("failed to create temp file for segment %04d: %w", segmentIndex, err)
	}
	defer outFile.Close()

	bytesWritten, err := io.Copy(outFile, resp.Body)
	if err != nil {
		os.Remove(tempFilepath) // Clean up temp file on error
		return fmt.Errorf("failed to write segment %04d: %w", segmentIndex, err)
	}

	// Close the file before renaming
	outFile.Close()

	// Atomically move temp file to final location
	if err := os.Rename(tempFilepath, filepath); err != nil {
		os.Remove(tempFilepath) // Clean up temp file on error
		return fmt.Errorf("failed to move temp file for segment %04d: %w", segmentIndex, err)
	}

	// Update overall progress
	progressTracker.UpdateSegment(segmentIndex, true, bytesWritten)
	return nil
}

// readM3U8File reads and returns the content of an m3u8 file
func readM3U8File(filepath string) (string, error) {
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		return "", fmt.Errorf("file does not exist: %s", filepath)
	}
	content, err := os.ReadFile(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", filepath, err)
	}
	return string(content), nil
}

// parseM3U8Content extracts segment URLs from m3u8 content
func parseM3U8Content(content string, baseURL string) ([]string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	lines := strings.Split(content, "\n")
	var segments []string
	fmt.Printf("📋 Parsing M3U8 content (%d lines)\n", len(lines))

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var segmentURL string

		if !strings.HasPrefix(line, "http") {
			rel, err := url.Parse(line)
			if err != nil {
				fmt.Printf("⚠️  Warning: Invalid URL on line %d: %s\n", lineNum+1, line)
				continue
			}
			segmentURL = base.ResolveReference(rel).String()
		} else {
			segmentURL = line
		}
		segments = append(segments, segmentURL)
	}
	fmt.Printf("🔍 Found %d segments\n", len(segments))
	return segments, nil
}

// OverallProgress tracks the overall download progress
type OverallProgress struct {
	totalSegments     int
	completedSegments int
	totalBytes        int64
	downloadedBytes   int64
	startTime         time.Time
	lastUpdate        time.Time
	mu                sync.Mutex
}

func NewOverallProgress(totalSegments int) *OverallProgress {
	return &OverallProgress{
		totalSegments: totalSegments,
		startTime:     time.Now(),
		lastUpdate:    time.Now(),
	}
}

func (op *OverallProgress) UpdateSegment(segmentIndex int, completed bool, bytes int64) {
	op.mu.Lock()
	defer op.mu.Unlock()

	if completed {
		op.completedSegments++
		op.downloadedBytes += bytes
	}

	// Update progress display every 100ms to avoid too frequent updates
	now := time.Now()
	if now.Sub(op.lastUpdate) > 100*time.Millisecond || op.completedSegments == op.totalSegments {
		//op.printProgress()
		op.lastUpdate = now
	}
}

func (op *OverallProgress) printProgress() {
	percentage := float64(op.completedSegments) / float64(op.totalSegments) * 100
	barWidth := 40
	filled := int(percentage / 100 * float64(barWidth))

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	elapsed := time.Since(op.startTime)
	speed := float64(op.downloadedBytes) / elapsed.Seconds()
	speedStr := formatBytes(int64(speed)) + "/s"

	fmt.Printf("\r📥 M3U8 Download: [%s] %.1f%% (%d/%d segments) %s %s",
		bar, percentage, op.completedSegments, op.totalSegments,
		formatBytes(op.downloadedBytes), speedStr)

	if op.completedSegments == op.totalSegments {
		fmt.Println() // New line when complete
	}
}

func DisplayAllProgress(tasks []*OverallProgress) {
	if len(tasks) == 0 {
		return
	}
	// Hide cursor to prevent flickering
	fmt.Print("\033[?25l")

	// Save current cursor position before we start printing
	fmt.Print("\033[s")

	for i, task := range tasks {
		if i > 0 {
			fmt.Print("\n") // Move to the next line for the next task
		}
		fmt.Print("\033[2K") // Clear current line
		task.printProgress()
	}

	fmt.Printf("\033[%dA", len(tasks)-1) // Move cursor up by (number of tasks - 1) lines
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// cleanupTempFiles removes temporary files created during the process
func cleanupTempFiles(files ...string) {
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			fmt.Printf("⚠️  Warning: Could not remove temp file %s: %v\n", file, err)
		}
	}
}

// countDownloadedSegments counts how many segment files were successfully downloaded
func countDownloadedSegments(outputDir string, totalSegments int) (int, error) {
	count := 0
	for i := 0; i < totalSegments; i++ {
		filename := fmt.Sprintf("segment_%04d.ts", i)
		fPath := filepath.Join(outputDir, filename)

		if info, err := os.Stat(fPath); err == nil && info.Size() > 0 {
			count++
		}
	}
	return count, nil
}

// downloadSegmentsConcurrently downloads segments using a worker pool with overall progress tracking
func downloadSegmentsConcurrently(segments []string, outputDir string, concurrentSegmentsCount int, progressTracker *OverallProgress) []error {
	// Create a channel for segment jobs
	jobs := make(chan struct {
		url   string
		index int
	}, len(segments))

	// Create a channel for results
	results := make(chan struct {
		index int
		err   error
	}, len(segments))

	// Start worker goroutines
	var wg sync.WaitGroup
	for w := 0; w < concurrentSegmentsCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				filename := fmt.Sprintf("segment_%04d.ts", job.index)
				fPath := filepath.Join(outputDir, filename)
				err := downloadSegment(job.url, fPath, job.index, progressTracker)
				results <- struct {
					index int
					err   error
				}{job.index, err}
			}
		}()
	}

	// Send jobs to workers
	go func() {
		for i, segmentURL := range segments {
			jobs <- struct {
				url   string
				index int
			}{segmentURL, i}
		}
		close(jobs)
	}()

	// Close results channel when all workers are done
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	errors := make([]error, len(segments))
	for result := range results {
		if result.err != nil {
			errors[result.index] = result.err
		}
	}

	return errors
}

func StartDownloadTask(m3u8URL string, outputDir string, concurrentSegmentsCount int) {
	// Create output directory
	if err := ensureDir(outputDir); err != nil {
		fmt.Printf("❌ Error creating directory: %v\n", err)
		os.Exit(1)
	}

	// Download the m3u8 playlist file
	fmt.Println("\n📥 Step 1: Downloading playlist...")
	playlistPath := filepath.Join(outputDir, "playlist.m3u8")
	defer os.Remove(playlistPath)
	if err := downloadFile(m3u8URL, playlistPath); err != nil {
		fmt.Printf("❌ Error downloading playlist: %v\n", err)
		os.Exit(1)
	}

	// Read the playlist content
	fmt.Println("\n📖 Step 2: Reading playlist...")
	content, err := readM3U8File(playlistPath)
	if err != nil {
		fmt.Printf("❌ Error reading playlist: %v\n", err)
		os.Exit(1)
	}

	// Parse the playlist to get segment URLs
	fmt.Println("\n🔍 Step 3: Parsing playlist...")
	segments, err := parseM3U8Content(content, m3u8URL)
	if err != nil {
		fmt.Printf("❌ Error parsing playlist: %v\n", err)
		os.Exit(1)
	}

	if len(segments) == 0 {
		fmt.Println("⚠️  No segments found in playlist")
		os.Exit(1)
	}

	fmt.Printf("\n⬇️  Step 4: Starting concurrent downloads (%d workers)...\n", concurrentSegmentsCount)
	startTime := time.Now()

	// Create overall progress tracker
	progressTracker := NewOverallProgress(len(segments))
	RunningTasks = append(RunningTasks, progressTracker)
	defer func() {
		for i, task := range RunningTasks {
			if task == progressTracker {
				RunningTasks = append(RunningTasks[:i], RunningTasks[i+1:]...)
				break
			}
		}
	}()

	// Download segments concurrently
	errors := downloadSegmentsConcurrently(segments, outputDir, concurrentSegmentsCount, progressTracker)

	// Count failed downloads
	failedCount := 0
	for i, err := range errors {
		if err != nil {
			failedCount++
			fmt.Printf("\n❌ Error downloading segment %04d: %v", i, err)
		}
	}

	// Check download results
	fmt.Println("\n📊 Step 5: Verifying downloads...")
	downloadedCount, err := countDownloadedSegments(outputDir, len(segments))
	if err != nil {
		fmt.Printf("⚠️  Warning: Could not verify downloads: %v\n", err)
	}

	duration := time.Since(startTime)

	// Final summary
	fmt.Println("\n🎉 Download Summary:")
	fmt.Printf("  📁 Output Directory: %s\n", outputDir)
	fmt.Printf("  📊 Total Segments: %d\n", len(segments))
	fmt.Printf("  ✅ Successfully Downloaded: %d\n", downloadedCount)
	fmt.Printf("  ❌ Failed Downloads: %d\n", failedCount)
	fmt.Printf("  ⚗️  Concurrent Workers: %d\n", concurrentSegmentsCount)
	fmt.Printf("  ⏱️  Total Time: %v\n", duration.Round(time.Second))

	if downloadedCount == len(segments) {
		fmt.Println("  🎯 All segments downloaded successfully!")
	} else {
		fmt.Printf("  ⚠️  %d segments may have failed\n", len(segments)-downloadedCount)
	}

	// Clean up any leftover temp files
	fmt.Println("\n🧹 Cleaning up temporary files...")
	tempPattern := filepath.Join(outputDir, "*.tmp")
	if matches, err := filepath.Glob(tempPattern); err == nil {
		cleanupTempFiles(matches...)
	}
}

func MergeFiles(outputDir string, outputName string, thumbnailPath string) {
	files, err := filepath.Glob(filepath.Join(outputDir, "*.ts"))
	if err != nil {
		fmt.Println("❌ Error finding .ts files: ")
		panic(err)
	}

	if len(files) == 0 {
		fmt.Println("No .ts files found in", outputDir)
		return
	}

	// Step 3: Sort files to preserve order
	sort.Strings(files)
	fmt.Println(files)

	// Step 4: Create FFmpeg concat list file
	concatFile := filepath.Join(outputDir, "concat_list.txt")
	f, err := os.Create(concatFile)
	if err != nil {
		fmt.Println("❌ Error creating concat list file:")
		panic(err)
	}
	defer f.Close()

	for _, file := range files {
		_, _ = f.WriteString(fmt.Sprintf("file '%s'\n", filepath.Base(file)))
	}
	fmt.Println("✅ Concat list created:", concatFile)

	// Step 5: Merge TS -> MP4 using FFmpeg (no re-encoding)
	cmd := exec.Command(
		"ffmpeg",
		"-f", "concat",
		"-safe", "0",
		"-i", concatFile,
		"-i", thumbnailPath,
		"-map", "0",
		"-map", "1",
		"-c", "copy",
		"-disposition:v:1", "attached_pic",
		outputName,
		"-y",
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("🚀 Merging segments into", outputName, "...")
	err = cmd.Run()
	if err != nil {
		fmt.Println("❌ Error merging files: ")
		panic(err)
	}

	fmt.Println("✅ Merge complete! Output file:", outputName)

	// Step 6: Cleanup original TS segments
	for _, ts := range files {
		_ = os.Remove(ts)
	}
	_ = os.Remove(concatFile)
	fmt.Println("🧹 Original TS segments and temp files deleted")
}
