package mediaprocess

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const maxPartSizeMB = 1900

func SplitVideoWithFFmpeg(inputPath string) ([]string, error) {
	// Get file info
	fileInfo, err := os.Stat(inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	fileSizeMB := fileInfo.Size() / (1024 * 1024)

	// If file is smaller than or equal to 1900MB, return original path
	if fileSizeMB <= maxPartSizeMB {
		return []string{inputPath}, nil
	}

	// Get video duration using FFprobe
	duration, err := getVideoDuration(inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get video duration: %w", err)
	}

	// Calculate number of parts and duration per part
	numParts := int((fileSizeMB + maxPartSizeMB - 1) / maxPartSizeMB)
	partDuration := duration / float64(numParts)

	// Get file extension and base name
	ext := filepath.Ext(inputPath)
	baseName := filepath.Base(inputPath)
	baseNameWithoutExt := baseName[:len(baseName)-len(ext)]
	dir := filepath.Dir(inputPath)

	var outputPaths []string

	for i := 0; i < numParts; i++ {
		startTime := float64(i) * partDuration

		// Create output file name
		partName := fmt.Sprintf("%s_part%d%s", baseNameWithoutExt, i+1, ext)
		partPath := filepath.Join(dir, partName)

		// Build FFmpeg command
		cmd := exec.Command("ffmpeg",
			"-i", inputPath,
			"-ss", formatTime(startTime),
			"-t", formatTime(partDuration),
			"-c", "copy", // Copy codecs without re-encoding
			"-avoid_negative_ts", "make_zero",
			partPath,
		)

		// Execute FFmpeg command
		output, err := cmd.CombinedOutput()
		if err != nil {
			CleanupFiles(outputPaths)
			return nil, fmt.Errorf("FFmpeg error for part %d: %v\nOutput: %s", i+1, err, output)
		}

		outputPaths = append(outputPaths, partPath)
	}

	return outputPaths, nil
}

func getVideoDuration(videoPath string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		videoPath,
	)

	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return 0, err
	}

	return duration, nil
}

func formatTime(seconds float64) string {
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	secs := seconds - float64(hours*3600+minutes*60)
	return fmt.Sprintf("%02d:%02d:%06.3f", hours, minutes, secs)
}

func CleanupFiles(paths []string) {
	for _, path := range paths {
		os.Remove(path)
	}
}

func main() {
	videoPath := "/path/to/your/video.mp4"

	parts, err := SplitVideoWithFFmpeg(videoPath)
	if err != nil {
		fmt.Printf("Error splitting video: %v\n", err)
		return
	}

	fmt.Printf("Video split into %d parts:\n", len(parts))
	for i, part := range parts {
		fmt.Printf("Part %d: %s\n", i+1, part)
	}
}
