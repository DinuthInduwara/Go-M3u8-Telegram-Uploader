package extractor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// M3U8Item represents a found M3U8 URL with metadata
type M3U8Item struct {
	URL       string `json:"url"`
	MimeType  string `json:"mime_type"`
	Bandwidth int    `json:"bandwidth,omitempty"`
}

// PlayerData represents extracted player information
type PlayerData struct {
	M3U8URLs  []M3U8Item `json:"m3u8_urls"`
	PosterURL string     `json:"poster_url,omitempty"`
	BestM3U8  string     `json:"best_m3u8,omitempty"`
}

// M3U8Playlist represents a playlist entry in master M3U8
type M3U8Playlist struct {
	URI       string
	Bandwidth int
}

// M3U8Grabber handles the extraction of M3U8 URLs from websites
type M3U8Grabber struct {
	timeout     time.Duration
	headless    bool
	m3u8URLs    []M3U8Item
	networkLogs []network.EventResponseReceived
	mutex       sync.RWMutex
}

// NewM3U8Grabber creates a new instance of M3U8Grabber
func NewM3U8Grabber(headless bool, timeoutSeconds int) *M3U8Grabber {
	return &M3U8Grabber{
		timeout:  time.Duration(timeoutSeconds) * time.Second,
		headless: headless,
		m3u8URLs: make([]M3U8Item, 0),
	}
}

// isM3U8URL checks if a URL is an M3U8 file
func isM3U8URL(urlStr, mimeType string) bool {
	if strings.HasSuffix(strings.ToLower(urlStr), ".m3u8") {
		return true
	}

	lowerMime := strings.ToLower(mimeType)
	if strings.Contains(lowerMime, "application/vnd.apple.mpegurl") ||
		strings.Contains(lowerMime, "application/x-mpegurl") {
		return true
	}

	return strings.Contains(strings.ToLower(urlStr), "m3u8")
}

// parseM3U8Master parses a master M3U8 playlist and returns playlists
func parseM3U8Master(content string) []M3U8Playlist {
	var playlists []M3U8Playlist
	lines := strings.Split(content, "\n")

	var currentBandwidth int
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			// Extract bandwidth
			bandwidthRegex := regexp.MustCompile(`BANDWIDTH=(\d+)`)
			matches := bandwidthRegex.FindStringSubmatch(line)
			if len(matches) > 1 {
				if bw, err := strconv.Atoi(matches[1]); err == nil {
					currentBandwidth = bw
				}
			}
		} else if line != "" && !strings.HasPrefix(line, "#") {
			// This is a playlist URL
			playlists = append(playlists, M3U8Playlist{
				URI:       line,
				Bandwidth: currentBandwidth,
			})
			currentBandwidth = 0
		}
	}

	return playlists
}

// getBestStreamURL fetches the master playlist and returns the best quality stream URL
func getBestStreamURL(masterURL string) (string, error) {
	fmt.Println("🔍 Analyzing master playlist for best quality...")

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Get(masterURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch master playlist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	playlists := parseM3U8Master(string(body))
	if len(playlists) == 0 {
		return "", fmt.Errorf("no stream variants found in master playlist")
	}

	// Find playlist with the highest bandwidth
	bestPlaylist := playlists[0]
	for _, playlist := range playlists[1:] {
		if playlist.Bandwidth > bestPlaylist.Bandwidth {
			bestPlaylist = playlist
		}
	}

	// Construct absolute URL if needed
	bestURL := bestPlaylist.URI
	if !strings.HasPrefix(bestURL, "http") {
		baseURL, err := url.Parse(masterURL)
		if err != nil {
			return "", fmt.Errorf("failed to parse master URL: %w", err)
		}
		resolvedURL, err := baseURL.Parse(bestPlaylist.URI)
		if err != nil {
			return "", fmt.Errorf("failed to resolve relative URL: %w", err)
		}
		bestURL = resolvedURL.String()
	}

	fmt.Printf("📊 Selected stream: %s (Bandwidth: %d)\n", bestURL, bestPlaylist.Bandwidth)
	return bestURL, nil
}

// extractPosterImage extracts poster image URLs from video elements
func (g *M3U8Grabber) extractPosterImage(ctx context.Context) string {
	fmt.Println("🖼️ Extracting poster image...")

	var posterURL string

	// JavaScript to extract poster URLs from various video player elements
	js := `
		(function() {
			var posterUrls = [];
			
			// Check video elements with poster attribute
			var videos = document.querySelectorAll('video[poster]');
			for (var i = 0; i < videos.length; i++) {
				if (videos[i].poster) {
					posterUrls.push(videos[i].poster);
				}
			}
			
			// Check data-poster attributes
			var dataPosterElements = document.querySelectorAll('[data-poster]');
			for (var i = 0; i < dataPosterElements.length; i++) {
				if (dataPosterElements[i].getAttribute('data-poster')) {
					posterUrls.push(dataPosterElements[i].getAttribute('data-poster'));
				}
			}
			
			// Check common video player containers
			var playerSelectors = [
				'.jwplayer[data-poster]',
				'.plyr[data-poster]',
				'.video-js[data-poster]',
				'[class*="player"][data-poster]',
				'[id*="player"][data-poster]',
				'.video-container[data-poster]',
				'.player-container[data-poster]'
			];
			
			for (var s = 0; s < playerSelectors.length; s++) {
				var elements = document.querySelectorAll(playerSelectors[s]);
				for (var i = 0; i < elements.length; i++) {
					var poster = elements[i].getAttribute('data-poster');
					if (poster) {
						posterUrls.push(poster);
					}
				}
			}
			
			// Check for poster in style attributes (background-image)
			var styledElements = document.querySelectorAll('[style*="background-image"]');
			for (var i = 0; i < styledElements.length; i++) {
				var style = styledElements[i].getAttribute('style');
				if (style) {
					var urlMatch = style.match(/background-image:\s*url\(['"]?([^'"]+)['"]?\)/);
					if (urlMatch && urlMatch[1]) {
						posterUrls.push(urlMatch[1]);
					}
				}
			}
			
			// Check for poster in CSS computed styles
			var videoContainers = document.querySelectorAll('.video-container, .player-container, [class*="video"], [class*="player"]');
			for (var i = 0; i < videoContainers.length; i++) {
				var computedStyle = window.getComputedStyle(videoContainers[i]);
				var bgImage = computedStyle.backgroundImage;
				if (bgImage && bgImage !== 'none') {
					var urlMatch = bgImage.match(/url\(['"]?([^'"]+)['"]?\)/);
					if (urlMatch && urlMatch[1]) {
						posterUrls.push(urlMatch[1]);
					}
				}
			}
			
			// Check meta tags for thumbnail/poster
			var metaTags = document.querySelectorAll('meta[property="og:image"], meta[name="twitter:image"], meta[property="og:image:url"]');
			for (var i = 0; i < metaTags.length; i++) {
				var content = metaTags[i].getAttribute('content');
				if (content) {
					posterUrls.push(content);
				}
			}
			
			// Filter out invalid URLs and return the first valid one
			for (var i = 0; i < posterUrls.length; i++) {
				var url = posterUrls[i].trim();
				if (url && (url.startsWith('http') || url.startsWith('//'))) {
					// Convert protocol-relative URLs to absolute
					if (url.startsWith('//')) {
						url = window.location.protocol + url;
					}
					return url;
				}
			}
			
			return '';
		})();
	`

	err := chromedp.Run(ctx,
		chromedp.Evaluate(js, &posterURL),
	)
	if err != nil {
		fmt.Printf("⚠️ Error extracting poster: %v\n", err)
		return ""
	}

	if posterURL != "" {
		fmt.Printf("🖼️ Found poster image: %s\n", posterURL)
	} else {
		fmt.Println("⚠️ No poster image found")
	}

	return posterURL
}

// extractM3U8FromLogs analyzes network logs for M3U8 URLs
func (g *M3U8Grabber) extractM3U8FromLogs() []M3U8Item {
	fmt.Println("🔍 Analyzing network requests for M3U8 URLs...")

	g.mutex.RLock()
	defer g.mutex.RUnlock()

	var m3u8Found []M3U8Item
	seenURLs := make(map[string]bool)

	for _, log := range g.networkLogs {
		urlStr := log.Response.URL
		mimeType := log.Response.MimeType

		if isM3U8URL(urlStr, mimeType) && !seenURLs[urlStr] {
			fmt.Printf("🎯 Found M3U8: %s\n", urlStr)
			m3u8Found = append(m3u8Found, M3U8Item{
				URL:      urlStr,
				MimeType: mimeType,
			})
			seenURLs[urlStr] = true
		}
	}

	return m3u8Found
}

// simulateUserInteraction simulates user interactions to trigger video loading
func (g *M3U8Grabber) simulateUserInteraction(ctx context.Context) error {
	fmt.Println("🖱️ Simulating user interactions...")

	// Wait for page to settle
	time.Sleep(3 * time.Second)

	// Scroll down to trigger lazy loading
	err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight/3);`, nil),
	)
	if err != nil {
		return err
	}
	time.Sleep(2 * time.Second)

	// Look for and click video containers
	videoSelectors := []string{
		"video",
		".video-container",
		".player-container",
		".video-wrapper",
		"[class*='video']",
		"[class*='player']",
		".jwplayer",
		".plyr",
	}

	for _, selector := range videoSelectors {
		err = chromedp.Run(ctx,
			chromedp.Click(selector, chromedp.NodeVisible, chromedp.ByQuery),
		)
		if err == nil {
			fmt.Printf("🎬 Clicked video container: %s\n", selector)
			time.Sleep(2 * time.Second)
			break
		}
	}

	// Try to find and click play buttons
	playSelectors := []string{
		".play-button",
		".btn-play",
		".play-btn",
		"[class*='play']",
		"[aria-label*='play' i]",
		"[aria-label*='Play']",
		"button[title*='play' i]",
		"button[title*='Play']",
		".video-overlay",
		".player-play-button",
		".vjs-big-play-button",
		".jwplayer-display-icon-container",
	}

	for _, selector := range playSelectors {
		err = chromedp.Run(ctx,
			chromedp.Click(selector, chromedp.NodeVisible, chromedp.ByQuery),
		)
		if err == nil {
			fmt.Printf("▶️ Clicked play button: %s\n", selector)
			time.Sleep(3 * time.Second)
			break
		}
	}

	// Additional interactions
	fmt.Println("🔄 Performing additional interactions...")

	// Simulate mouse movement
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`
			var event = new MouseEvent('mouseover', {
				view: window,
				bubbles: true,
				cancelable: true
			});
			document.body.dispatchEvent(event);
		`, nil),
	)
	if err != nil {
		return err
	}
	time.Sleep(1 * time.Second)

	// Focus window
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`window.focus();`, nil),
	)
	if err != nil {
		return err
	}
	time.Sleep(1 * time.Second)

	// Scroll back to top
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`window.scrollTo(0, 0);`, nil),
	)
	if err != nil {
		return err
	}
	time.Sleep(2 * time.Second)

	// Click on page body
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`document.body.click();`, nil),
	)
	if err != nil {
		return err
	}
	time.Sleep(2 * time.Second)

	return nil
}

// GrabM3U8URL is the main method to grab M3U8 URLs from a website
func (g *M3U8Grabber) GrabM3U8URL(targetURL string) (*PlayerData, error) {
	fmt.Println("🚀 Starting optimized Chrome browser...")

	// Set up Chrome options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", g.headless),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("disable-features", "VizDisplayCompositor"),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1920, 1080),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
		// Block heavy resources
		chromedp.Flag("blink-settings", "imagesEnabled=false"),
		chromedp.Flag("disable-plugins", true),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Set timeout
	ctx, cancel = context.WithTimeout(ctx, g.timeout)
	defer cancel()

	// Enable network events
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *network.EventResponseReceived:
			g.mutex.Lock()
			g.networkLogs = append(g.networkLogs, *ev)
			g.mutex.Unlock()
		}
	})

	fmt.Println("✅ Browser started successfully")
	fmt.Printf("🌐 Navigating to: %s\n", targetURL)

	// Navigate to page and wait for body
	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(targetURL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to page: %w", err)
	}

	fmt.Println("✅ Page loaded successfully")

	// Wait for video player elements
	fmt.Println("⏳ Waiting for video player to load...")
	videoSelectors := []string{
		"video",
		".video-player",
		".player",
		"#video",
		"[class*='player']",
		"[id*='player']",
		".jwplayer",
		".plyr",
	}

	for _, selector := range videoSelectors {
		err = chromedp.Run(ctx,
			chromedp.WaitVisible(selector, chromedp.ByQuery),
		)
		if err == nil {
			fmt.Printf("✅ Found video element: %s\n", selector)
			break
		}
	}
	if err != nil {
		fmt.Println("⚠️ No video player found, continuing anyway...")
	}

	// Extract poster image first
	posterURL := g.extractPosterImage(ctx)

	// Simulate user interactions
	err = g.simulateUserInteraction(ctx)
	if err != nil {
		fmt.Printf("⚠️ Error during interaction: %v\n", err)
	}

	// Check for M3U8 URLs multiple times with delays
	fmt.Println("⏳ Waiting for M3U8 requests...")
	var m3u8URLs []M3U8Item
	maxAttempts := 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		fmt.Printf("🔍 Attempt %d/%d - Analyzing network requests...\n", attempt+1, maxAttempts)
		time.Sleep(5 * time.Second)

		foundURLs := g.extractM3U8FromLogs()
		if len(foundURLs) > 0 {
			m3u8URLs = foundURLs
			break
		}

		if attempt < maxAttempts-1 {
			fmt.Println("🔄 No M3U8 found yet, trying more interactions...")
			// Additional interactions
			err = chromedp.Run(ctx,
				chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight);`, nil),
			)
			if err == nil {
				time.Sleep(2 * time.Second)
				chromedp.Run(ctx,
					chromedp.Evaluate(`window.scrollTo(0, 0);`, nil),
				)
				time.Sleep(1 * time.Second)

				// Try clicking random elements
				chromedp.Run(ctx,
					chromedp.Evaluate(`
						var elements = document.querySelectorAll('div, span, button, a');
						for(var i = 0; i < Math.min(5, elements.length); i++) {
							if(elements[i].offsetParent !== null) {
								elements[i].click();
								break;
							}
						}
					`, nil),
				)
				time.Sleep(2 * time.Second)
			}
		}
	}

	// Create PlayerData struct
	playerData := &PlayerData{
		M3U8URLs:  m3u8URLs,
		PosterURL: posterURL,
	}

	if len(m3u8URLs) > 0 {
		fmt.Printf("🎉 Successfully found %d M3U8 URL(s)!\n", len(m3u8URLs))
		for i, item := range m3u8URLs {
			fmt.Printf("  %d. %s\n", i+1, item.URL)
		}

		// Try to get the best stream URL
		for _, item := range m3u8URLs {
			bestURL, err := getBestStreamURL(item.URL)
			if err == nil {
				playerData.BestM3U8 = bestURL
				break
			}
		}

		// If no best URL found, use the first M3U8 URL
		if playerData.BestM3U8 == "" && len(m3u8URLs) > 0 {
			playerData.BestM3U8 = m3u8URLs[0].URL
		}
	} else {
		fmt.Println("😞 No M3U8 URLs found")
	}

	return playerData, nil
}

// GetPlayerData extracts both M3U8 URLs and poster image from a website
func GetPlayerData(targetURL string) (*PlayerData, error) {
	fmt.Println("🎯 M3U8 Link & Poster Grabber")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Target URL: %s\n", targetURL)
	fmt.Println(strings.Repeat("=", 50))

	// Create grabber instance
	grabber := NewM3U8Grabber(true, 30)

	// Grab player data
	playerData, err := grabber.GrabM3U8URL(targetURL)
	if err != nil {
		fmt.Printf("❌ Error occurred: %v\n", err)
		return nil, err
	}

	// Output results
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("📋 RESULTS:")

	if len(playerData.M3U8URLs) > 0 {
		fmt.Printf("✅ Found %d M3U8 URL(s):\n", len(playerData.M3U8URLs))

		for i, item := range playerData.M3U8URLs {
			fmt.Printf("\n%d. URL: %s\n", i+1, item.URL)
			fmt.Printf("   MIME Type: %s\n", item.MimeType)
		}

		if playerData.BestM3U8 != "" {
			fmt.Printf("\n🏆 Best Quality Stream: %s\n", playerData.BestM3U8)
		}
	} else {
		fmt.Println("❌ No M3U8 URLs found")
	}

	if playerData.PosterURL != "" {
		fmt.Printf("\n🖼️ Poster Image: %s\n", playerData.PosterURL)
	} else {
		fmt.Println("\n⚠️ No poster image found")
	}

	if len(playerData.M3U8URLs) == 0 {
		fmt.Println("\n💡 Troubleshooting tips:")
		fmt.Println("  1. Check if the website is accessible")
		fmt.Println("  2. The video might require additional user interaction")
		fmt.Println("  3. Try running with headless=false to see what's happening")
		fmt.Println("  4. The website might be using a different video player")
	}

	return playerData, nil
}

// Legacy function for backward compatibility
func GetVideoDetails(targetURL string) (string, string, error) {
	playerData, err := GetPlayerData(targetURL)
	if err != nil {
		return "", "", err
	}

	if playerData.BestM3U8 != "" {
		return playerData.BestM3U8, playerData.PosterURL, nil
	}

	if len(playerData.M3U8URLs) > 0 {
		return playerData.M3U8URLs[0].URL, playerData.PosterURL, nil
	}

	return "", "", fmt.Errorf("no m3u8 found")
}
