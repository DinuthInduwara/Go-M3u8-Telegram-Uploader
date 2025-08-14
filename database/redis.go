package database

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var RedisCLI *redis.Client

func init() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("Warning: .env not loaded, using system env variables")
	}

	RedisCLI = redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDIS_ADDR"),
		Username: os.Getenv("REDIS_USER"),
		Password: os.Getenv("REDIS_PASS"),
	})
}
func IsDownloaded(videoID string) bool {
	ctx := context.Background()
	fmt.Println("Checking if video is downloaded:", videoID)
	exists, err := RedisCLI.SIsMember(ctx, "downloaded_videos", videoID).Result()
	if err != nil {
		panic(err)
	}

	if exists {
		fmt.Println("Already downloaded")
	} else{
		fmt.Println(videoID, "Not downloaded yet")
	}
	return exists
}
func MarkAsDownloaded(videoID string) {
	ctx := context.Background()
	if err := RedisCLI.SAdd(ctx, "downloaded_videos", videoID).Err(); err != nil {
		panic(err)
	}
	fmt.Println("Marked as downloaded:", videoID)
}
