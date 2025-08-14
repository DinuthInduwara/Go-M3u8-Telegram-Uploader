package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

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

type progressBar struct{}

// Chunk implements uploader.Progress interface
func (p *progressBar) Chunk(ctx context.Context, state uploader.ProgressState) error {
	percent := float64(state.Uploaded) / float64(state.Total) * 100
	fmt.Printf("\r[%-30s] %.1f%% (%.2f/%.2f MB)",
		strings.Repeat("█", int(percent/100*30)),
		percent,
		float64(state.Uploaded)/(1024*1024),
		float64(state.Total)/(1024*1024),
	)
	if state.Uploaded == state.Total {
		fmt.Println()
	}
	return nil
}

func UploadVideo(client *gotgproto.Client, config Config) error {
	ctx := context.Background()

	// Get file info
	fileInfo, err := os.Stat(config.VideoPath)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	fileName := filepath.Base(config.VideoPath)
	fileSize := fileInfo.Size()

	fmt.Printf("Uploading video: %s (%.2f MB)\n", fileName, float64(fileSize)/(1024*1024))

	f, err := os.Open(config.VideoPath)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Println("Starting upload...")

	videoFile, err := uploader.NewUploader(client.API()).WithPartSize(524288).WithProgress(&progressBar{}).FromFile(ctx, f)

	if err != nil {
		return fmt.Errorf("failed to upload video file: %w", err)
	}
	fmt.Println("Video file uploaded to Telegram servers")

	// Prepare attributes
	attributes := []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: fileName},
		&tg.DocumentAttributeVideo{
			Duration:          0,
			W:                 0,
			H:                 0,
			RoundMessage:      false,
			SupportsStreaming: true,
		},
	}
	mimeType := getMimeType(config.VideoPath)

	// Prepare media
	media := &tg.InputMediaUploadedDocument{
		File:       videoFile,
		MimeType:   mimeType,
		Attributes: attributes,
	}

	// Thumbnail upload (if provided)
	if config.Thumbnail != "" {
		if _, err := os.Stat(config.Thumbnail); err == nil {
			fmt.Println("Uploading thumbnail...")
			thumbFile, err := uploader.NewUploader(client.API()).FromPath(ctx, config.Thumbnail)
			if err != nil {
				log.Printf("Warning: Failed to upload thumbnail: %v", err)
			} else {
				media.Thumb = thumbFile
				fmt.Println("Thumbnail uploaded")
			}
		}
	}

	// Send video
	peerStorage := client.PeerStorage
	inputPeer := peerStorage.GetInputPeerById(config.ChatID)
	if inputPeer == nil {
		return fmt.Errorf("failed to resolve chat ID: %d", config.ChatID)
	}
	var randomID int64
	binary.Read(rand.Reader, binary.LittleEndian, &randomID)

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
