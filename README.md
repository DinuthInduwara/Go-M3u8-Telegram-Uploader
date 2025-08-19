# 🎬 Go-M3u8-Downloader

A blazing-fast, concurrent M3U8 video downloader and Telegram uploader written in Go! 🚀

## Features ✨

-   🧩 Extracts M3U8 URLs from any website (even dynamic ones)
-   ⚡ Concurrent segment downloads for maximum speed
-   🎥 Merges video segments into MP4 automatically
-   📤 Uploads videos (with thumbnails) directly to Telegram
-   📊 Real-time pipeline statistics and progress bars
-   🛠️ Robust error handling and graceful shutdown

## Prerequisites 🛎️

-   Go 1.18+
-   FFmpeg installed and available in your PATH
-   Telegram API credentials (API ID, API Hash, Phone Number)

## Getting Started 🚦

### 1. Clone the Repository

```powershell
git clone https://github.com/DinuthInduwara/Go-M3u8-Downloader.git
cd Go-M3u8-Downloader
```

### 2. Install Dependencies

```powershell
go mod tidy
```

### 3. Configure Environment Variables

Create a `.env` file in the root directory with your Telegram credentials:

```env
API_ID=your_api_id
API_HASH=your_api_hash
PHONE_NUMBER=your_phone_number
SESSION=your_session_string
TARGET_CHAT_ID=your_telegram_chat_id
```

### 4. Build the Project

```powershell
go build -o go_build_Go_M3u8_Downloader.exe main.go
```

### 5. Run the Downloader

```powershell
./go_build_Go_M3u8_Downloader.exe
```

## Usage 📝

1. **Enter the number of download workers** (for concurrency).
2. **Input a video page URL** or provide a `.txt` file with multiple URLs.
3. The pipeline will:
    - Extract the M3U8 playlist and thumbnail
    - Download all video segments concurrently
    - Merge segments into a single MP4 file
    - Upload the video (with thumbnail) to your Telegram chat
4. Watch the real-time progress and stats in your terminal! 📊

## Example: Batch Download from URLs File 📂

Prepare a text file (e.g., `urls.txt`) with one video page URL per line:

```
https://example.com/video1
https://example.com/video2
```

Run:

```powershell
./go_build_Go_M3u8_Downloader.exe urls.txt
```

## Troubleshooting 🛠️

-   Make sure FFmpeg is installed and accessible from your terminal.
-   Ensure your Telegram API credentials are correct.
-   For dynamic sites, the extractor uses a headless Chrome browser (chromedp). Chrome must be installed.

## Contributing 🤝

Pull requests and suggestions are welcome! Feel free to open issues for bugs or feature requests.

## License 📄

MIT

---

Made with ❤️ by Dinuth Induwara
