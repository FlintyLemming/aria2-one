const fs = require('fs');
const path = require('path');

let playwright;
try {
  playwright = require('playwright');
} catch (e) {
  playwright = require('/tmp/playwright-test/node_modules/playwright');
}
const { chromium } = playwright;

const SCREENSHOT_DIR = path.resolve(__dirname, '../screenshots');
if (!fs.existsSync(SCREENSHOT_DIR)) {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true });
}

async function runTest() {
  console.log('--- Starting Playwright E2E Test ---');
  const browser = await chromium.launch({
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox']
  });

  const context = await browser.newContext({
    httpCredentials: {
      username: 'admin',
      password: 'admin888',
    },
    viewport: { width: 1280, height: 800 },
    acceptDownloads: true,
  });

  const page = await context.newPage();

  // Log console and errors from browser
  page.on('console', msg => {
    console.log(`[BROWSER ${msg.type().toUpperCase()}]`, msg.text());
  });
  page.on('pageerror', err => {
    console.error('[BROWSER ERROR]', err.message);
  });

  try {
    // 1. Visit homepage
    console.log('1. Navigating to http://127.0.0.1:7815/ ...');
    await page.goto('http://127.0.0.1:7815/', { waitUntil: 'networkidle' });
    await page.waitForTimeout(2000);

    const title = await page.title();
    console.log('Page Title:', title);

    // Save screenshot 1: Homepage
    const shot1 = path.join(SCREENSHOT_DIR, '01-homepage.png');
    await page.screenshot({ path: shot1, fullPage: true });
    console.log('Screenshot saved:', shot1);

    // 2. Click "+ New" button
    console.log('2. Clicking "+ New" button...');
    const newBtnSelector = 'a[ng-href="#!/new"], a[href="#!/new"]';
    await page.waitForSelector(newBtnSelector, { timeout: 5000 });
    await page.click(newBtnSelector);

    // Wait for the new task page/modal to render
    console.log('Waiting for new task form (textarea[name="urls"])...');
    await page.waitForSelector('textarea[name="urls"]', { timeout: 5000 });
    await page.waitForTimeout(1000);

    // Save screenshot 2: New Task Page
    const shot2 = path.join(SCREENSHOT_DIR, '02-new-task-page.png');
    await page.screenshot({ path: shot2, fullPage: true });
    console.log('Screenshot saved:', shot2);

    // 3. Fill URL and Submit Download
    const testDownloadUrl = 'https://httpbin.org/robots.txt';
    console.log(`3. Filling download URL: ${testDownloadUrl}...`);
    await page.fill('textarea[name="urls"]', testDownloadUrl);
    await page.waitForTimeout(1000);

    // Save screenshot 3: URL Filled
    const shot3 = path.join(SCREENSHOT_DIR, '03-url-filled.png');
    await page.screenshot({ path: shot3, fullPage: true });
    console.log('Screenshot saved:', shot3);

    console.log('Clicking "Download Now" button...');
    const submitBtnSelector = 'button[type="submit"]';
    await page.click(submitBtnSelector);

    // AriaNg will redirect to active or waiting list, or we can go to stopped list
    console.log('Waiting for task submission and download completion...');
    await page.waitForTimeout(3000);

    // Save screenshot 4: Task Submitted / In Progress
    const shot4 = path.join(SCREENSHOT_DIR, '04-task-submitted.png');
    await page.screenshot({ path: shot4, fullPage: true });
    console.log('Screenshot saved:', shot4);

    // 4. Navigate to Completed / Stopped list
    console.log('Navigating to Stopped / Completed list (#!/stopped)...');
    const stoppedNavSelector = 'a[ng-href="#!/stopped"], a[href="#!/stopped"]';
    if (await page.$(stoppedNavSelector)) {
      await page.click(stoppedNavSelector);
    } else {
      await page.goto('http://127.0.0.1:7815/#!/stopped', { waitUntil: 'networkidle' });
    }
    await page.waitForTimeout(3000);

    // Check if the completed task shows the "⬇️ 下载到本地" button
    console.log('Waiting for completed task row with direct download link...');
    const directDownloadSelector = 'a.btn-direct-download';
    await page.waitForSelector(directDownloadSelector, { timeout: 10000 });

    const downloadLinkText = await page.innerText(directDownloadSelector);
    console.log('Found direct download link text:', downloadLinkText);

    // Save screenshot 5: Task completed with Direct Download Button
    const shot5 = path.join(SCREENSHOT_DIR, '05-task-completed-with-direct-link.png');
    await page.screenshot({ path: shot5, fullPage: true });
    console.log('Screenshot saved:', shot5);

    // 5. Test clicking "⬇️ 下载到本地" and verify browser download
    console.log('5. Clicking "⬇️ 下载到本地" to trigger browser download...');
    const [download] = await Promise.all([
      page.waitForEvent('download', { timeout: 10000 }),
      page.click(directDownloadSelector),
    ]);

    const suggestedFilename = download.suggestedFilename();
    console.log('Suggested download filename:', suggestedFilename);

    const localSavePath = path.join(SCREENSHOT_DIR, suggestedFilename || 'downloaded-robots.txt');
    await download.saveAs(localSavePath);
    console.log('File successfully downloaded to local path:', localSavePath);

    const fileContent = fs.readFileSync(localSavePath, 'utf8');
    console.log('Downloaded file content:\n' + fileContent);

    // Save screenshot 6: Final state after successful download
    const shot6 = path.join(SCREENSHOT_DIR, '06-download-completed-locally.png');
    await page.screenshot({ path: shot6, fullPage: true });
    console.log('Screenshot saved:', shot6);

    console.log('--- ALL E2E PLAYWRIGHT TESTS PASSED SUCCESSFULLY! ---');
  } catch (err) {
    console.error('Test failed with error:', err);
    const errShot = path.join(SCREENSHOT_DIR, 'error.png');
    await page.screenshot({ path: errShot, fullPage: true }).catch(() => {});
    throw err;
  } finally {
    await browser.close();
  }
}

runTest().catch(err => {
  console.error('Fatal error during test:', err);
  process.exit(1);
});
