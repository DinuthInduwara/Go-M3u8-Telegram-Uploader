package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/sessionMaker"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

type Config struct {
	SessionDB string
	VideoPath string
	ChatID    int64
	Caption   string
	Thumbnail string // Optional thumbnail path
}

// ProgressReader wraps an io.Reader and provides progress tracking
type ProgressReader struct {
	reader         io.Reader
	total          int64
	current        int64
	lastUpdate     time.Time
	updateInterval time.Duration
}

// NewProgressReader creates a new progress reader
func NewProgressReader(reader io.Reader, total int64) *ProgressReader {
	return &ProgressReader{
		reader:         reader,
		total:          total,
		current:        0,
		lastUpdate:     time.Now(),
		updateInterval: 100 * time.Millisecond, // Update every 100ms
	}
}

// Read implements io.Reader interface and tracks progress
func (pr *ProgressReader) Read(p []byte) (n int, err error) {
	n, err = pr.reader.Read(p)
	pr.current += int64(n)

	// Update progress display
	now := time.Now()
	if now.Sub(pr.lastUpdate) >= pr.updateInterval || err == io.EOF {
		pr.displayProgress()
		pr.lastUpdate = now
	}

	return n, err
}

// displayProgress shows the current upload progress
func (pr *ProgressReader) displayProgress() {
	if pr.total == 0 {
		fmt.Printf("\rUploaded: %.2f MB", float64(pr.current)/(1024*1024))
		return
	}

	percent := float64(pr.current) / float64(pr.total) * 100
	uploadedMB := float64(pr.current) / (1024 * 1024)
	totalMB := float64(pr.total) / (1024 * 1024)

	// Create progress bar
	barWidth := 30
	filled := int(percent / 100 * float64(barWidth))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

	fmt.Printf("\r[%s] %.1f%% (%.2f/%.2f MB)", bar, percent, uploadedMB, totalMB)

	if percent >= 100 {
		fmt.Println() // New line when complete
	}
}

func NewTelegramClient(apiIDInt int, apiHash string, phoneNumber string) *gotgproto.Client {

	client, err := gotgproto.NewClient(
		apiIDInt,
		apiHash,
		gotgproto.ClientTypePhone(phoneNumber),
		&gotgproto.ClientOpts{
			Session: sessionMaker.TelethonSession(os.Getenv("SESSION")).
				Name("my_session"),
		},
	)
	if err != nil {
		panic(err)
	}
	return client
}

func getMimeType(filename string) string {
	ext := filepath.Ext(filename)
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	default:
		return "video/mp4" // Default to mp4
	}
}

func UploadVideo(client *gotgproto.Client, config Config) error {
	// Create context
	ctx := context.Background()

	// Get file info
	fileInfo, err := os.Stat(config.VideoPath)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	fileName := filepath.Base(config.VideoPath)
	fileSize := fileInfo.Size()

	fmt.Printf("Uploading video: %s (%.2f MB)\n", fileName, float64(fileSize)/(1024*1024))

	// Open the file
	file, err := os.Open(config.VideoPath)
	if err != nil {
		return fmt.Errorf("failed to open video file: %w", err)
	}
	defer file.Close()

	// Create uploader instance
	u := uploader.NewUploader(client.API())

	f, _ := os.Open(config.VideoPath)
	defer f.Close()

	stat, _ := f.Stat()

	progress := NewProgressReader(f, stat.Size())

	videoFile, err := u.FromReader(ctx, fileName, io.TeeReader(progress, io.Discard))
	if err != nil {
		fmt.Println() // New line after progress bar
		return fmt.Errorf("failed to upload video file: %w", err)
	}

	fmt.Println("Video file uploaded to Telegram servers")

	// Prepare document attributes
	attributes := []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{
			FileName: fileName,
		},
		&tg.DocumentAttributeVideo{
			Duration:          0, // Set to 0 if unknown
			W:                 0, // Width - set to 0 if unknown
			H:                 0, // Height - set to 0 if unknown
			RoundMessage:      false,
			SupportsStreaming: true,
		},
	}

	// Get MIME type based on file extension
	mimeType := getMimeType(config.VideoPath)

	// Prepare media object
	media := &tg.InputMediaUploadedDocument{
		File:       videoFile,
		MimeType:   mimeType,
		Attributes: attributes,
	}

	// Handle thumbnail if provided
	if config.Thumbnail != "" {
		if _, err := os.Stat(config.Thumbnail); err == nil {
			fmt.Println("Uploading thumbnail...")
			thumbFile, err := u.FromPath(ctx, config.Thumbnail)
			if err != nil {
				log.Printf("Warning: Failed to upload thumbnail: %v", err)
			} else {
				media.Thumb = thumbFile
				fmt.Println("Thumbnail uploaded")
			}
		}
	}

	// Get peer storage for chat ID resolution
	peerStorage := client.PeerStorage
	inputPeer := peerStorage.GetInputPeerById(config.ChatID)

	if inputPeer == nil {
		return fmt.Errorf("failed to resolve chat ID: %d. Make sure the bot/user has access to this chat", config.ChatID)
	}

	var randomID int64
	binary.Read(rand.Reader, binary.LittleEndian, &randomID)

	// Send the video
	fmt.Println("Sending video message...")
	_, err = client.API().MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer:     inputPeer,
		Media:    media,
		Message:  config.Caption,
		RandomID: randomID,
	})

	if err != nil {
		return fmt.Errorf("failed to send video: %w", err)
	}

	return nil
}
