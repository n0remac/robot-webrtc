const { test, expect, devices, chromium } = require('@playwright/test');

// ✅ All device pairings you want to test
const scenarios = [
  {
    name: 'Desktop ↔ Desktop',
    devices: ['Desktop Chrome', 'Desktop Chrome'],
    room: 'desktop-desktop',
    names: ['Alice', 'Bob'],
  },
  {
    name: 'Pixel ↔ Desktop',
    devices: ['Pixel 5', 'Desktop Chrome'],
    room: 'pixel-desktop',
    names: ['Pixel', 'Desktop'],
  },
  {
    name: 'Android ↔ iPhone',
    devices: ['Pixel 5', 'iPhone 12'],
    room: 'android-iphone',
    names: ['Android', 'iPhone'],
  },
  {
    name: 'iPhone ↔ Desktop',
    devices: ['iPhone 12', 'Desktop Chrome'],
    room: 'iphone-desktop',
    names: ['iPhone', 'Desktop'],
  },
];

// ✅ Reusable test function
async function runWebRTCJoinFlowTest(deviceA, deviceB, room, nameA, nameB) {
  const BASE_URL = `http://localhost:8080/video`;
  // const BASE_URL = `https://noremac.dev/video`;

  const browser = await chromium.launch({ headless: false });

  const launchContext = async (device) =>
    await browser.newContext({
      ...devices[device],
      permissions: ['camera', 'microphone'],
      launchOptions: {
        args: [
          '--use-fake-ui-for-media-stream',
          '--use-fake-device-for-media-stream',
        ],
      },
    });

  const contextA = await launchContext(deviceA);
  const contextB = await launchContext(deviceB);

  const pageA = await contextA.newPage();
  const pageB = await contextB.newPage();

  await pageA.goto(BASE_URL);
  await pageA.waitForTimeout(500);
  await pageB.goto(BASE_URL);

  await pageA.fill('#name', nameA);
  await pageB.fill('#name', nameB);

  await pageA.click('#join-btn');
  await pageB.click('#join-btn');

  await pageA.waitForTimeout(3000);

  await expect(pageA.locator('#local-video')).toBeVisible();
  await expect(pageB.locator('#local-video')).toBeVisible();
  await expect(pageA.locator('.remote-video')).toBeVisible();
  await expect(pageB.locator('.remote-video')).toBeVisible();

  // ❌ Close pageB (simulate disconnect)
  await pageB.evaluate(() => {
    window.dispatchEvent(new Event('beforeunload'));
  });

  // ⏳ Wait for the remaining page to update
  await pageA.waitForTimeout(2000);

  await contextB.close();

  // ✅ Check that the remote video was removed from pageA
  const remoteCount = await pageA.locator('.remote-video').count();
  expect(remoteCount).toBe(0);

  await contextA.close();
  await browser.close();
}

// 🔁 Run all scenarios
for (const { name, devices: [deviceA, deviceB], room, names: [nameA, nameB] } of scenarios) {
  test(name, async () => {
    await new Promise(resolve => setTimeout(resolve, 2000));
    await runWebRTCJoinFlowTest(deviceA, deviceB, room, nameA, nameB);
  });
}
