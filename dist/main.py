#!/usr/bin/env python3
"""
M3U8 Link Grabber
Efficiently extracts M3U8 URLs from websites without loading heavy resources
"""

from urllib.parse import urljoin
import requests
import m3u8
import time
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.chrome.service import Service
from webdriver_manager.chrome import ChromeDriverManager
from selenium.common.exceptions import TimeoutException, WebDriverException
import json
import sys
import io


sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')


def get_best_stream_url(master_url: str) -> str:
    response = requests.get(master_url)
    response.raise_for_status()  # Ensure we stop on HTTP errors

    master_playlist = m3u8.loads(response.text)

    if not master_playlist.playlists:
        raise ValueError("No stream variants found in the master playlist.")

    # Find the playlist with the highest bandwidth
    best_playlist = max(
        master_playlist.playlists,
        key=lambda p: p.stream_info.bandwidth if p.stream_info.bandwidth is not None else 0
    )

    # Construct absolute URL if needed (relative URLs are common)
    best_url = urljoin(master_url, best_playlist.uri)

    return best_url


class M3U8Grabber:
    def __init__(self, headless=True, timeout=30):
        """
        Initialize the M3U8 grabber with optimized browser settings

        Args:
            headless (bool): Run browser in headless mode
            timeout (int): Maximum wait time in seconds
        """
        self.timeout = timeout
        self.driver = None
        self.m3u8_urls = []

        # Set up Chrome options for maximum performance
        self.chrome_options = Options()

        if headless:
            self.chrome_options.add_argument('--headless')

        # Performance optimizations
        self.chrome_options.add_argument('--no-sandbox')
        self.chrome_options.add_argument('--disable-dev-shm-usage')
        self.chrome_options.add_argument('--disable-gpu')
        self.chrome_options.add_argument('--disable-web-security')
        self.chrome_options.add_argument(
            '--disable-features=VizDisplayCompositor')

        # Anti-detection measures for headless mode
        if headless:
            self.chrome_options.add_argument(
                '--disable-blink-features=AutomationControlled')
            self.chrome_options.add_experimental_option(
                "excludeSwitches", ["enable-automation"])
            self.chrome_options.add_experimental_option(
                'useAutomationExtension', False)
            # Set a realistic window size
            self.chrome_options.add_argument('--window-size=1920,1080')
            # Add user agent to look more like a real browser
            self.chrome_options.add_argument(
                '--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36')

        # Block heavy resources to speed up loading
        prefs = {
            "profile.managed_default_content_settings.images": 2,  # Block images
            "profile.managed_default_content_settings.plugins": 2,  # Block plugins
            "profile.managed_default_content_settings.popups": 2,  # Block popups
            "profile.managed_default_content_settings.geolocation": 2,  # Block location
            "profile.managed_default_content_settings.notifications": 2,  # Block notifications
            "profile.managed_default_content_settings.media_stream": 2,  # Block media
        }
        self.chrome_options.add_experimental_option("prefs", prefs)

        # Set up logging to capture network requests
        self.chrome_options.add_argument('--enable-logging')
        self.chrome_options.add_argument('--log-level=0')

        # Enable performance logging for network monitoring
        self.chrome_options.add_argument('--enable-logging')
        self.chrome_options.add_argument('--log-level=0')
        self.chrome_options.set_capability(
            'goog:loggingPrefs', {'performance': 'ALL'})

    def start_driver(self):
        """Initialize the Chrome WebDriver"""
        try:
            print("🚀 Starting optimized Chrome browser...")

            # Auto-install ChromeDriver if not found
            try:
                service = Service(ChromeDriverManager().install())
                self.driver = webdriver.Chrome(
                    service=service, options=self.chrome_options)
            except Exception:
                # Fallback to system ChromeDriver
                print("📦 Using system ChromeDriver...")
                self.driver = webdriver.Chrome(options=self.chrome_options)

            # Anti-detection measures
            self.driver.execute_script(
                "Object.defineProperty(navigator, 'webdriver', {get: () => undefined})")

            # Enable network monitoring
            self.driver.execute_cdp_cmd('Network.enable', {})
            print("✅ Browser started successfully")
            return True

        except WebDriverException as e:
            print(f"❌ Failed to start browser: {e}")
            print("💡 Installing ChromeDriver automatically...")
            print("💡 If this fails, install manually: pip install webdriver-manager")
            return False

    def extract_m3u8_from_logs(self):
        """Extract M3U8 URLs from browser performance logs"""
        print("🔍 Analyzing network requests for M3U8 URLs...")

        try:
            logs = self.driver.get_log('performance')
            m3u8_found = []

            for entry in logs:
                try:
                    message = json.loads(entry['message'])

                    # Look for network response events
                    if message['message']['method'] == 'Network.responseReceived':
                        response = message['message']['params']['response']
                        url = response.get('url', '')
                        mime_type = response.get('mimeType', '')

                        # Check if it's an M3U8 file
                        if (url.endswith('.m3u8') or
                            'application/vnd.apple.mpegurl' in mime_type or
                            'application/x-mpegURL' in mime_type or
                                'm3u8' in url.lower()):

                            print(f"🎯 Found M3U8: {url}")
                            m3u8_found.append({
                                'url': url,
                                'mime_type': mime_type,
                                'timestamp': entry['timestamp']
                            })

                except (json.JSONDecodeError, KeyError):
                    continue

            # Remove duplicates and sort by timestamp
            unique_m3u8 = []
            seen_urls = set()

            for item in sorted(m3u8_found, key=lambda x: x['timestamp']):
                if item['url'] not in seen_urls:
                    unique_m3u8.append(item)
                    seen_urls.add(item['url'])

            return unique_m3u8

        except Exception as e:
            print(f"⚠️ Error analyzing logs: {e}")
            return []

    def wait_for_video_player(self):
        """Wait for video player elements to appear"""
        print("⏳ Waiting for video player to load...")

        # Common video player selectors
        video_selectors = [
            'video',
            '.video-player',
            '.player',
            '#video',
            '[class*="player"]',
            '[id*="player"]',
            '.jwplayer',
            '.plyr',
        ]

        for selector in video_selectors:
            try:
                WebDriverWait(self.driver, 5).until(
                    EC.presence_of_element_located((By.CSS_SELECTOR, selector))
                )
                print(f"✅ Found video element: {selector}")
                return True
            except TimeoutException:
                continue

        print("⚠️ No video player found, continuing anyway...")
        return False

    def simulate_user_interaction(self):
        """Simulate user interactions to trigger video loading"""
        print("🖱️ Simulating user interactions...")

        try:
            # Wait a bit for page to settle
            time.sleep(3)

            # Scroll down to trigger lazy loading
            self.driver.execute_script(
                "window.scrollTo(0, document.body.scrollHeight/3);")
            time.sleep(2)

            # Look for common video containers and click them
            video_containers = [
                '.video-container',
                '.player-container',
                '.video-wrapper',
                '[class*="video"]',
                '[class*="player"]',
                'video',
                '.jwplayer',
                '.plyr',
            ]

            for selector in video_containers:
                try:
                    elements = self.driver.find_elements(
                        By.CSS_SELECTOR, selector)
                    for element in elements:
                        if element.is_displayed():
                            print(f"🎬 Clicking video container: {selector}")
                            # Use JavaScript click to avoid interception
                            self.driver.execute_script(
                                "arguments[0].click();", element)
                            time.sleep(2)
                            break
                except:
                    continue

            # Try to find and click play button with more selectors
            play_selectors = [
                '.play-button',
                '.btn-play',
                '.play-btn',
                '[class*="play"]',
                '[aria-label*="play"]',
                '[aria-label*="Play"]',
                'button[title*="play"]',
                'button[title*="Play"]',
                '.video-overlay',
                '.player-play-button',
                '.vjs-big-play-button',
                '.jwplayer-display-icon-container',
            ]

            for selector in play_selectors:
                try:
                    play_buttons = self.driver.find_elements(
                        By.CSS_SELECTOR, selector)
                    for play_button in play_buttons:
                        if play_button.is_displayed():
                            print(f"▶️  Clicking play button: {selector}")
                            # Try both regular and JavaScript click
                            try:
                                play_button.click()
                            except:
                                self.driver.execute_script(
                                    "arguments[0].click();", play_button)
                            time.sleep(3)
                            break
                except:
                    continue

            # Additional interactions to trigger video loading
            print("🔄 Performing additional interactions...")

            # Simulate mouse movement over video area
            self.driver.execute_script("""
                var event = new MouseEvent('mouseover', {
                    view: window,
                    bubbles: true,
                    cancelable: true
                });
                document.body.dispatchEvent(event);
            """)
            time.sleep(1)

            # Simulate focus events
            self.driver.execute_script("window.focus();")
            time.sleep(1)

            # Scroll back to top
            self.driver.execute_script("window.scrollTo(0, 0);")
            time.sleep(2)

            # Try clicking on the page body to ensure focus
            self.driver.execute_script("document.body.click();")
            time.sleep(2)

        except Exception as e:
            print(f"⚠️ Error during interaction: {e}")

    def grab_m3u8_url(self, url):
        """
        Main method to grab M3U8 URL from the given website

        Args:
            url (str): Website URL to visit

        Returns:
            list: List of found M3U8 URLs
        """
        if not self.start_driver():
            return []

        try:
            print(f"🌐 Navigating to: {url}")

            # Navigate to the page
            self.driver.get(url)

            # Wait for initial page load
            WebDriverWait(self.driver, self.timeout).until(
                EC.presence_of_element_located((By.TAG_NAME, "body"))
            )
            print("✅ Page loaded successfully")

            # Wait for video player
            self.wait_for_video_player()

            # Simulate user interactions
            self.simulate_user_interaction()

            # Wait a bit more for network requests and try multiple times
            print("⏳ Waiting for M3U8 requests...")

            # Check for M3U8 URLs multiple times with delays
            m3u8_urls = []
            max_attempts = 3

            for attempt in range(max_attempts):
                print(
                    f"🔍 Attempt {attempt + 1}/{max_attempts} - Analyzing network requests...")
                time.sleep(5)  # Wait longer between attempts

                # Extract M3U8 URLs from logs
                found_urls = self.extract_m3u8_from_logs()

                if found_urls:
                    m3u8_urls = found_urls
                    break

                if attempt < max_attempts - 1:  # Don't interact on last attempt
                    print("🔄 No M3U8 found yet, trying more interactions...")
                    # Try additional interactions
                    self.driver.execute_script(
                        "window.scrollTo(0, document.body.scrollHeight);")
                    time.sleep(2)
                    self.driver.execute_script("window.scrollTo(0, 0);")
                    time.sleep(1)

                    # Try clicking anywhere on the page
                    self.driver.execute_script("""
                        var elements = document.querySelectorAll('div, span, button, a');
                        for(var i = 0; i < Math.min(5, elements.length); i++) {
                            if(elements[i].offsetParent !== null) {
                                elements[i].click();
                                break;
                            }
                        }
                    """)
                    time.sleep(2)

            if m3u8_urls:
                print(f"🎉 Successfully found {len(m3u8_urls)} M3U8 URL(s)!")
                for i, item in enumerate(m3u8_urls, 1):
                    print(f"  {i}. {item['url']}")
            else:
                print("😞 No M3U8 URLs found")

            return m3u8_urls

        except TimeoutException:
            print(f"⏰ Timeout after {self.timeout} seconds")
            return []

        except Exception as e:
            print(f"❌ Error occurred: {e}")
            return []

        finally:
            self.cleanup()

    def cleanup(self):
        """Clean up resources"""
        if self.driver:
            print("🧹 Closing browser...")
            self.driver.quit()
            print("✅ Browser closed")


def main():
    """Main function"""
    if len(sys.argv) > 1:
        target_url = sys.argv[1]
        text_file = sys.argv[2]
    else:
        sys.exit(1)

    print("🎯 M3U8 Link Grabber")
    print("=" * 50)
    print(f"Target URL: {target_url}")
    print("=" * 50)

    # Create grabber instance
    grabber = M3U8Grabber(headless=True, timeout=30)

    # Grab M3U8 URLs
    m3u8_urls = grabber.grab_m3u8_url(target_url)

    # Output results
    print("\n" + "=" * 50)
    print("📋 RESULTS:")

    if m3u8_urls:
        print(f"✅ Found {len(m3u8_urls)} M3U8 URL(s):")

        for i, item in enumerate(m3u8_urls, 1):
            print(f"\n{i}. URL: {item['url']}")
            print(f"   MIME Type: {item.get('mime_type', 'N/A')}")

        # Save to file
        for item in m3u8_urls:
            if not item['url'].endswith('playlist.m3u8'):
                print(f"⚠️ Skipping non-M3U8 URL: {item['url']}")
                continue
            url = get_best_stream_url(item['url'])
            with open(text_file, 'a', encoding='utf-8') as f:
                f.write(f"{url}\n")
                print(f"✅ Saved to {text_file}")

        # Show command for Go downloader
        if m3u8_urls:
            best_url = m3u8_urls[0]['url']  # Use first/best URL
            print(f"\n🚀 Ready for Go downloader:")
            print(f"go run main.go \"{best_url}\"")
            sys.exit(0)

    else:
        print("❌ No M3U8 URLs found")
        print("\n💡 Troubleshooting tips:")
        print("  1. Check if the website is accessible")
        print("  2. The video might require additional user interaction")
        print("  3. Try running with headless=False to see what's happening")
        print("  4. The website might be using a different video player")
        sys.exit(1)


if __name__ == "__main__":
    main()
